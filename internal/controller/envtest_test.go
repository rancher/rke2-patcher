package controller

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/rancher/rke2-patcher/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// TestEnvtest runs the controller against a real kube-apiserver. It is skipped unless
// KUBEBUILDER_ASSETS points to envtest binaries, e.g.:
//
//	export KUBEBUILDER_ASSETS=$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@latest use -p path)
func TestEnvtest(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set")
	}

	ctrl.SetLogger(klog.NewKlogr())

	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	helmControllerDir, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/k3s-io/helm-controller").Output()
	if err != nil {
		t.Fatalf("locate helm-controller module: %v", err)
	}

	env := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join(repoRoot, "charts", "rke2-patcher", "crds"),
			filepath.Join(strings.TrimSpace(string(helmControllerDir)), "pkg", "crds", "yaml", "generated"),
		},
		ErrorIfCRDPathMissing: true,
	}
	config, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	scheme := k8sruntime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = v1alpha1.AddToScheme(scheme)
	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	t.Run("CEL rejects a name that differs from the component", func(t *testing.T) {
		ip := imagePatch("rke2-traefik", targetTag)
		ip.Name = "my-traefik"
		err := c.Create(ctx, ip)
		if err == nil || !strings.Contains(err.Error(), "metadata.name must equal spec.component") {
			t.Fatalf("expected CEL rejection, got: %v", err)
		}
	})

	t.Run("CEL rejects an unsupported component", func(t *testing.T) {
		err := c.Create(ctx, imagePatch("rke2-unknown", targetTag))
		if err == nil || !k8serrors.IsInvalid(err) {
			t.Fatalf("expected enum rejection, got: %v", err)
		}
	})

	t.Run("upgradePolicy defaults to Manual", func(t *testing.T) {
		ip := imagePatch("rke2-metrics-server", "v0.8.0-build20260901")
		ip.Spec.UpgradePolicy = ""
		if err := c.Create(ctx, ip); err != nil {
			t.Fatal(err)
		}
		if ip.Spec.UpgradePolicy != v1alpha1.UpgradePolicyManual {
			t.Fatalf("upgradePolicy = %q", ip.Spec.UpgradePolicy)
		}
		_ = c.Delete(ctx, ip)
	})

	t.Run("manager reconciles create and delete", func(t *testing.T) {
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "rke2-patcher"}}); err != nil && !k8serrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}

		mgr, err := ctrl.NewManager(config, ctrl.Options{
			Scheme:  scheme,
			Cache:   CacheOptions("rke2-patcher"),
			Metrics: metricsserver.Options{BindAddress: "0"},
		})
		if err != nil {
			t.Fatal(err)
		}

		cluster := newFakeCluster()
		reconciler := &Reconciler{
			Client:   mgr.GetClient(),
			Recorder: mgr.GetEventRecorder("rke2-patcher"),
			Ops:      cluster.ops(),
		}
		if err := reconciler.SetupWithManager(mgr); err != nil {
			t.Fatal(err)
		}

		mgrCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- mgr.Start(mgrCtx) }()
		stopped := false
		stop := func() {
			if !stopped {
				stopped = true
				cancel()
				<-done
			}
		}
		t.Cleanup(stop)

		if err := c.Create(ctx, imagePatch("rke2-traefik", targetTag)); err != nil {
			t.Fatal(err)
		}

		eventually(t, func() bool {
			var ip v1alpha1.ImagePatch
			if err := c.Get(ctx, types.NamespacedName{Name: "rke2-traefik"}, &ip); err != nil {
				return false
			}
			return ip.Status.AppliedTag == targetTag && len(ip.Finalizers) == 1
		})

		var ip v1alpha1.ImagePatch
		if err := c.Get(ctx, types.NamespacedName{Name: "rke2-traefik"}, &ip); err != nil {
			t.Fatal(err)
		}
		if err := c.Delete(ctx, &ip); err != nil {
			t.Fatal(err)
		}
		eventually(t, func() bool {
			err := c.Get(ctx, types.NamespacedName{Name: "rke2-traefik"}, &v1alpha1.ImagePatch{})
			return k8serrors.IsNotFound(err)
		})

		// Inspect the fake cluster only once the manager goroutine is gone
		stop()
		if _, found := cluster.hccValue(t, "rke2-traefik", "image", "tag"); found {
			t.Fatal("delete did not revert the HelmChartConfig")
		}
		if _, found := cluster.hccAnnotation(t, "rke2-traefik", managedTraefik); found {
			t.Fatal("delete did not remove the managed annotation")
		}
	})
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("condition not met within 20s")
}
