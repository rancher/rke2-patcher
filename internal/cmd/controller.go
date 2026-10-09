package cmd

import (
	"fmt"

	v1alpha1 "github.com/rancher/rke2-patcher/api/v1alpha1"
	"github.com/rancher/rke2-patcher/internal/controller"
	"github.com/rancher/rke2-patcher/internal/state"
	cli "github.com/urfave/cli/v2"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// runControllerCommand runs the ImagePatch controller until it receives a termination signal
func runControllerCommand(ctx *cli.Context) error {
	ctrl.SetLogger(klog.NewKlogr())

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}

	config, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load kubernetes config: %w", err)
	}

	namespace := state.Namespace()
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:                  scheme,
		Cache:                   controller.CacheOptions(namespace),
		LeaderElection:          ctx.Bool("leader-elect"),
		LeaderElectionID:        "rke2-patcher-controller",
		LeaderElectionNamespace: namespace,
		HealthProbeBindAddress:  ctx.String("health-probe-bind-address"),
		Metrics:                 metricsserver.Options{BindAddress: ctx.String("metrics-bind-address")},
	})
	if err != nil {
		return fmt.Errorf("failed to create controller manager: %w", err)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}

	reconciler := &controller.Reconciler{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorder("rke2-patcher"),
		Ops:      controller.DefaultOperations(namespace),
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed to set up ImagePatch controller: %w", err)
	}

	return mgr.Start(ctrl.SetupSignalHandler())
}
