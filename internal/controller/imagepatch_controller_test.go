package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	helmcontrollerv1 "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	v1alpha1 "github.com/rancher/rke2-patcher/api/v1alpha1"
	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/kube"
	"github.com/rancher/rke2-patcher/internal/patcher"
	"github.com/rancher/rke2-patcher/internal/state"
	"gopkg.in/yaml.v3"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	syaml "sigs.k8s.io/yaml"
)

const (
	testVersion     = "v1.34.2+rke2r1"
	upgradedVersion = "v1.34.3+rke2r1"
	baselineTag     = "v3.3.4-build20260801"
	targetTag       = "v3.3.6-build20260912"
	newerTag        = "v3.3.7-build20260920"
	outOfWindow     = "v3.3.8-build20261020"
	newMinorTag     = "v3.4.0-build20260901"
	traefikImage    = "rancher/hardened-traefik"
	managedTraefik  = v1alpha1.ManagedAnnotationPrefix + "rke2-traefik"
)

// fakeCluster is an in-memory cluster, registry and HelmChartConfig store
type fakeCluster struct {
	version string
	prime   bool
	images  []string
	zeroDay time.Time
	tags    []string
	charts  map[string]string // chart name -> bundled values.yaml
	cli     state.State

	hcc   map[string]*kube.HelmChartConfigObject
	hccRV int
}

func newFakeCluster() *fakeCluster {
	return &fakeCluster{
		version: testVersion,
		prime:   true,
		images:  []string{traefikImage + ":" + baselineTag},
		zeroDay: time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
		tags:    []string{outOfWindow, newerTag, targetTag, baselineTag, newMinorTag},
		charts: map[string]string{
			"rke2-traefik": "image:\n  repository: rancher/hardened-traefik\n  tag: " + baselineTag + "\n",
			"rke2-coredns": "image:\n  repository: rancher/hardened-coredns\n  tag: v1.14.2-build20260310\n" +
				"autoscaler:\n  image:\n    repository: rancher/hardened-cluster-autoscaler\n    tag: v1.10.1-build20260801\n" +
				"nodelocal:\n  image:\n    repository: rancher/hardened-dns-node-cache\n    tag: 1.26.7-build20260310\n",
		},
		cli: state.State{Entries: map[string]state.Entry{}},
		hcc: map[string]*kube.HelmChartConfigObject{},
	}
}

func (f *fakeCluster) ops() Operations {
	return Operations{
		ClusterVersion: func() (string, error) { return f.version, nil },
		PrimeEnabled:   func(string) (bool, error) { return f.prime, nil },
		RunningImages: func(components.Component) ([]kube.PodImageSummary, error) {
			summaries := make([]kube.PodImageSummary, 0, len(f.images))
			for _, image := range f.images {
				summaries = append(summaries, kube.PodImageSummary{Image: image, Count: 1})
			}
			return summaries, nil
		},
		ClusterZeroDay: func() (time.Time, error) { return f.zeroDay, nil },
		ListTags:       func(string) ([]string, error) { return f.tags, nil },
		BundledChartValues: func(chartName string) (map[string]any, error) {
			values := map[string]any{}
			if err := yaml.Unmarshal([]byte(f.charts[chartName]), &values); err != nil {
				return nil, err
			}
			return values, nil
		},
		GetHelmChartConfig: func(name string, _ string) (*kube.HelmChartConfigObject, error) {
			obj, found := f.hcc[name]
			if !found {
				return nil, nil
			}
			copied := *obj
			return &copied, nil
		},
		ApplyHelmChartConfig: func(content string, resourceVersion string) error {
			name, namespace, err := patcher.HelmChartConfigIdentityFromContent(content)
			if err != nil {
				return err
			}
			gr := schema.GroupResource{Group: "helm.cattle.io", Resource: "helmchartconfigs"}
			existing, found := f.hcc[name]
			if resourceVersion == "" && found {
				return k8serrors.NewAlreadyExists(gr, name)
			}
			if resourceVersion != "" && (!found || existing.ResourceVersion != resourceVersion) {
				return k8serrors.NewConflict(gr, name, fmt.Errorf("stale resourceVersion"))
			}
			f.hccRV++
			f.hcc[name] = &kube.HelmChartConfigObject{Name: name, Namespace: namespace, Content: content, ResourceVersion: strconv.Itoa(f.hccRV)}
			return nil
		},
		LoadCLIState: func() (state.State, error) { return f.cli, nil },
	}
}

// setHCC stores a HelmChartConfig with the given (de-indented) valuesContent and annotations
func (f *fakeCluster) setHCC(name string, values string, annotations map[string]string) {
	f.hccRV++
	hcc := helmcontrollerv1.HelmChartConfig{
		TypeMeta:   metav1.TypeMeta{APIVersion: "helm.cattle.io/v1", Kind: "HelmChartConfig"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system", Annotations: annotations},
		Spec:       helmcontrollerv1.HelmChartConfigSpec{ValuesContent: values},
	}
	raw, _ := syaml.Marshal(hcc)
	f.hcc[name] = &kube.HelmChartConfigObject{Name: name, Namespace: "kube-system", Content: string(raw), ResourceVersion: strconv.Itoa(f.hccRV)}
}

func (f *fakeCluster) parseHCC(t *testing.T, name string) (*helmcontrollerv1.HelmChartConfig, bool) {
	t.Helper()
	obj, found := f.hcc[name]
	if !found {
		return nil, false
	}
	var hcc helmcontrollerv1.HelmChartConfig
	if err := syaml.Unmarshal([]byte(obj.Content), &hcc); err != nil {
		t.Fatalf("invalid HelmChartConfig: %v", err)
	}
	return &hcc, true
}

// hccValue returns the value at a dotted path of the HelmChartConfig's valuesContent
func (f *fakeCluster) hccValue(t *testing.T, name string, path ...string) (any, bool) {
	t.Helper()
	hcc, found := f.parseHCC(t, name)
	if !found {
		return nil, false
	}
	values := map[string]any{}
	if err := yaml.Unmarshal([]byte(hcc.Spec.ValuesContent), &values); err != nil {
		t.Fatalf("invalid valuesContent: %v", err)
	}
	var current any = values
	for _, key := range path {
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		if current, ok = asMap[key]; !ok {
			return nil, false
		}
	}
	return current, true
}

func (f *fakeCluster) hccAnnotation(t *testing.T, name string, key string) (string, bool) {
	t.Helper()
	hcc, found := f.parseHCC(t, name)
	if !found {
		return "", false
	}
	value, found := hcc.Annotations[key]
	return value, found
}

type harness struct {
	t       *testing.T
	cluster *fakeCluster
	client  client.Client
	r       *Reconciler
}

func newHarness(t *testing.T, objects ...client.Object) *harness {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&v1alpha1.ImagePatch{}).Build()
	cluster := newFakeCluster()
	return &harness{t: t, cluster: cluster, client: c, r: &Reconciler{Client: c, Ops: cluster.ops()}}
}

func imagePatch(component string, tag string) *v1alpha1.ImagePatch {
	return &v1alpha1.ImagePatch{
		ObjectMeta: metav1.ObjectMeta{Name: component},
		Spec:       v1alpha1.ImagePatchSpec{Component: component, Tag: tag, UpgradePolicy: v1alpha1.UpgradePolicyManual},
	}
}

func (h *harness) reconcile(name string) ctrl.Result {
	h.t.Helper()
	result, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
	if err != nil {
		h.t.Fatalf("reconcile returned error: %v", err)
	}
	return result
}

func (h *harness) get(name string) *v1alpha1.ImagePatch {
	h.t.Helper()
	var ip v1alpha1.ImagePatch
	if err := h.client.Get(context.Background(), types.NamespacedName{Name: name}, &ip); err != nil {
		h.t.Fatalf("get ImagePatch: %v", err)
	}
	return &ip
}

func (h *harness) update(ip *v1alpha1.ImagePatch) {
	h.t.Helper()
	if err := h.client.Update(context.Background(), ip); err != nil {
		h.t.Fatalf("update ImagePatch: %v", err)
	}
}

func (h *harness) setTag(name string, tag string) {
	h.t.Helper()
	ip := h.get(name)
	ip.Spec.Tag = tag
	h.update(ip)
}

func (h *harness) delete(name string) {
	h.t.Helper()
	if err := h.client.Delete(context.Background(), h.get(name)); err != nil {
		h.t.Fatalf("delete ImagePatch: %v", err)
	}
}

func (h *harness) requireGone(name string) {
	h.t.Helper()
	var ip v1alpha1.ImagePatch
	if err := h.client.Get(context.Background(), types.NamespacedName{Name: name}, &ip); !k8serrors.IsNotFound(err) {
		h.t.Fatalf("ImagePatch still exists after finalization: %v", err)
	}
}

func requireCondition(t *testing.T, ip *v1alpha1.ImagePatch, conditionType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	condition := meta.FindStatusCondition(ip.Status.Conditions, conditionType)
	if condition == nil {
		t.Fatalf("condition %s missing; conditions: %+v", conditionType, ip.Status.Conditions)
	}
	if condition.Status != status || (reason != "" && condition.Reason != reason) {
		t.Fatalf("condition %s = %s/%s (%s), want %s/%s", conditionType, condition.Status, condition.Reason, condition.Message, status, reason)
	}
}

func TestPatchAppliesTagAnnotatesAndTracksRollout(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-traefik", targetTag))

	result := h.reconcile("rke2-traefik")

	if tag, _ := h.cluster.hccValue(t, "rke2-traefik", "image", "tag"); tag != targetTag {
		t.Fatalf("HelmChartConfig image.tag = %v, want %s", tag, targetTag)
	}
	if version, _ := h.cluster.hccAnnotation(t, "rke2-traefik", managedTraefik); version != testVersion {
		t.Fatalf("managed annotation = %q, want %s", version, testVersion)
	}

	ip := h.get("rke2-traefik")
	if ip.Status.AppliedTag != targetTag || ip.Status.BaselineTag != baselineTag || ip.Status.ClusterVersion != testVersion {
		t.Fatalf("unexpected status: %+v", ip.Status)
	}
	if len(ip.Finalizers) != 1 || ip.Finalizers[0] != v1alpha1.Finalizer {
		t.Fatalf("finalizer not added: %v", ip.Finalizers)
	}
	requireCondition(t, ip, v1alpha1.ConditionApplied, metav1.ConditionTrue, "Applied")
	requireCondition(t, ip, v1alpha1.ConditionReady, metav1.ConditionFalse, "RolloutInProgress")
	if result.RequeueAfter != rolloutRequeue {
		t.Fatalf("RequeueAfter = %v, want %v while rolling out", result.RequeueAfter, rolloutRequeue)
	}

	h.cluster.images = []string{traefikImage + ":" + targetTag}
	h.reconcile("rke2-traefik")

	ip = h.get("rke2-traefik")
	requireCondition(t, ip, v1alpha1.ConditionRolledOut, metav1.ConditionTrue, "RolledOut")
	requireCondition(t, ip, v1alpha1.ConditionReady, metav1.ConditionTrue, "Patched")
}

// The baseline comes from the chart, not from the running pods: it stays right while a
// patched image is running and does not depend on status
func TestBaselineComesFromTheBundledChart(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-traefik", newerTag))
	h.cluster.images = []string{traefikImage + ":" + targetTag}

	h.reconcile("rke2-traefik")

	if ip := h.get("rke2-traefik"); ip.Status.BaselineTag != baselineTag {
		t.Fatalf("baseline = %q, want the chart's %s", ip.Status.BaselineTag, baselineTag)
	}
}

func TestPatchIsBlockedByPolicy(t *testing.T) {
	cases := []struct {
		name   string
		tag    string
		prime  bool
		reason string
	}{
		{name: "outside patch window", tag: outOfWindow, prime: true, reason: "OutsidePatchWindow"},
		{name: "new minor", tag: newMinorTag, prime: true, reason: "MinorVersionChange"},
		{name: "unknown tag", tag: "v3.3.9-build20260905", prime: true, reason: "TagNotFound"},
		{name: "not prime", tag: targetTag, prime: false, reason: "NotPrime"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, imagePatch("rke2-traefik", tc.tag))
			h.cluster.prime = tc.prime

			h.reconcile("rke2-traefik")

			ip := h.get("rke2-traefik")
			requireCondition(t, ip, v1alpha1.ConditionBlocked, metav1.ConditionTrue, tc.reason)
			requireCondition(t, ip, v1alpha1.ConditionReady, metav1.ConditionFalse, tc.reason)
			if len(h.cluster.hcc) != 0 {
				t.Fatalf("blocked patch changed the cluster: %v", h.cluster.hcc)
			}
		})
	}
}

func TestCLIPatchesBlockTheController(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-traefik", targetTag))
	h.cluster.cli.Entries[testVersion+"|rke2-coredns"] = state.Entry{Component: "rke2-coredns", ClusterVersion: testVersion}

	h.reconcile("rke2-traefik")

	requireCondition(t, h.get("rke2-traefik"), v1alpha1.ConditionBlocked, metav1.ConditionTrue, "CLIPatchesExist")
	if len(h.cluster.hcc) != 0 {
		t.Fatalf("blocked patch changed the cluster: %v", h.cluster.hcc)
	}

	// Once the CLI patch is reconciled the ImagePatch proceeds
	h.cluster.cli.Entries = map[string]state.Entry{}
	h.reconcile("rke2-traefik")
	requireCondition(t, h.get("rke2-traefik"), v1alpha1.ConditionApplied, metav1.ConditionTrue, "Applied")
}

// rke2-dns-node-cache would write CoreDNS's image key: the chart layout check refuses it
func TestComponentWithMismatchedChartLayoutIsBlocked(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-dns-node-cache", "1.26.8-build20260416"))

	h.reconcile("rke2-dns-node-cache")

	requireCondition(t, h.get("rke2-dns-node-cache"), v1alpha1.ConditionBlocked, metav1.ConditionTrue, "ChartLayoutMismatch")
	if len(h.cluster.hcc) != 0 {
		t.Fatalf("blocked patch changed the cluster: %v", h.cluster.hcc)
	}
}

func TestChangingTagRepatches(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-traefik", targetTag))
	h.reconcile("rke2-traefik")
	h.cluster.images = []string{traefikImage + ":" + targetTag}

	h.setTag("rke2-traefik", newerTag)
	h.reconcile("rke2-traefik")

	if tag, _ := h.cluster.hccValue(t, "rke2-traefik", "image", "tag"); tag != newerTag {
		t.Fatalf("HelmChartConfig image.tag = %v, want %s", tag, newerTag)
	}
	if ip := h.get("rke2-traefik"); ip.Status.AppliedTag != newerTag || ip.Status.BaselineTag != baselineTag {
		t.Fatalf("unexpected status: %+v", ip.Status)
	}
}

// Like the CLI: no moving back below the applied tag; rolling back goes through the bundled tag
func TestOlderTagThanAppliedIsRefused(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-traefik", newerTag))
	h.reconcile("rke2-traefik")

	h.setTag("rke2-traefik", targetTag)
	h.reconcile("rke2-traefik")

	ip := h.get("rke2-traefik")
	requireCondition(t, ip, v1alpha1.ConditionBlocked, metav1.ConditionTrue, "NotNewer")
	requireMessage(t, ip, v1alpha1.ConditionBlocked, `refusing to patch: requested target tag "`+targetTag+`" is older than current tag "`+newerTag+`"`)
	if tag, _ := h.cluster.hccValue(t, "rke2-traefik", "image", "tag"); tag != newerTag {
		t.Fatalf("refused rollback changed the HelmChartConfig: %v", tag)
	}

	// Two steps: back to the bundled tag, then the older patch
	h.setTag("rke2-traefik", baselineTag)
	h.reconcile("rke2-traefik")
	h.setTag("rke2-traefik", targetTag)
	h.reconcile("rke2-traefik")
	if tag, _ := h.cluster.hccValue(t, "rke2-traefik", "image", "tag"); tag != targetTag {
		t.Fatalf("rollback through the bundled tag failed, image.tag = %v", tag)
	}
}

func TestTagEqualToBaselineRevertsPatch(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-traefik", targetTag))
	h.reconcile("rke2-traefik")

	h.setTag("rke2-traefik", baselineTag)
	h.reconcile("rke2-traefik")

	if _, found := h.cluster.hccValue(t, "rke2-traefik", "image", "tag"); found {
		t.Fatal("patcher values still present after reverting to the baseline")
	}
	if _, found := h.cluster.hccAnnotation(t, "rke2-traefik", managedTraefik); found {
		t.Fatal("managed annotation still present after reverting to the baseline")
	}
	requireCondition(t, h.get("rke2-traefik"), v1alpha1.ConditionReady, metav1.ConditionTrue, "AtBaseline")
}

func TestManualOverrideBlocksPatch(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-traefik", targetTag))
	h.cluster.setHCC("rke2-traefik", "image:\n  tag: v3.3.5-custom\nports:\n  web: 8080\n", nil)

	h.reconcile("rke2-traefik")

	requireCondition(t, h.get("rke2-traefik"), v1alpha1.ConditionBlocked, metav1.ConditionTrue, "ConflictingOverride")
	if tag, _ := h.cluster.hccValue(t, "rke2-traefik", "image", "tag"); tag != "v3.3.5-custom" {
		t.Fatalf("manual override was overwritten: %v", tag)
	}

	// Deleting a blocked ImagePatch must not strip the user's own values
	h.delete("rke2-traefik")
	h.reconcile("rke2-traefik")
	h.requireGone("rke2-traefik")
	if tag, _ := h.cluster.hccValue(t, "rke2-traefik", "image", "tag"); tag != "v3.3.5-custom" {
		t.Fatalf("deleting an unmanaged ImagePatch reverted the user's override: %v", tag)
	}
}

func TestMergesWithUnrelatedValuesAndAnnotations(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-traefik", targetTag))
	h.cluster.setHCC("rke2-traefik", "ports:\n  web: 8080\n", map[string]string{"team": "qa"})

	h.reconcile("rke2-traefik")

	if port, _ := h.cluster.hccValue(t, "rke2-traefik", "ports", "web"); port != 8080 {
		t.Fatalf("unrelated value lost: %v", port)
	}
	if team, _ := h.cluster.hccAnnotation(t, "rke2-traefik", "team"); team != "qa" {
		t.Fatalf("user annotation lost: %q", team)
	}
	if tag, _ := h.cluster.hccValue(t, "rke2-traefik", "image", "tag"); tag != targetTag {
		t.Fatalf("HelmChartConfig image.tag = %v, want %s", tag, targetTag)
	}
}

func TestDriftIsCorrected(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-traefik", targetTag))
	h.reconcile("rke2-traefik")

	// Someone rewrites the values; the annotation stays (kubectl apply keeps unknown annotations)
	h.cluster.setHCC("rke2-traefik", "ports:\n  web: 8080\n", map[string]string{managedTraefik: testVersion})
	h.reconcile("rke2-traefik")

	if tag, _ := h.cluster.hccValue(t, "rke2-traefik", "image", "tag"); tag != targetTag {
		t.Fatalf("drift not corrected, image.tag = %v", tag)
	}
	if port, _ := h.cluster.hccValue(t, "rke2-traefik", "ports", "web"); port != 8080 {
		t.Fatalf("drift correction dropped the user's values: %v", port)
	}
}

func TestUpgradeWithManualPolicyMarksStaleAndTouchesNothing(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-traefik", targetTag))
	h.reconcile("rke2-traefik")
	before := h.cluster.hcc["rke2-traefik"].Content

	h.cluster.version = upgradedVersion
	result := h.reconcile("rke2-traefik")

	ip := h.get("rke2-traefik")
	requireCondition(t, ip, v1alpha1.ConditionStale, metav1.ConditionTrue, "RKE2Upgraded")
	requireCondition(t, ip, v1alpha1.ConditionReady, metav1.ConditionFalse, "RKE2Upgraded")
	requireMessage(t, ip, v1alpha1.ConditionStale, `refusing to patch: active patch for component "rke2-traefik" from RKE2 `+testVersion)
	if h.cluster.hcc["rke2-traefik"].Content != before {
		t.Fatal("Manual policy modified the HelmChartConfig")
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("stale ImagePatch should wait for an event, got RequeueAfter %v", result.RequeueAfter)
	}

	// Deleting it reverts the patch
	h.delete("rke2-traefik")
	h.reconcile("rke2-traefik")
	if _, found := h.cluster.hccValue(t, "rke2-traefik", "image", "tag"); found {
		t.Fatal("deleting a stale ImagePatch did not revert it")
	}
}

func TestStalePatchOfAnotherComponentBlocksNewPatches(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-traefik", targetTag))
	h.cluster.setHCC("rke2-coredns", "image:\n  repository: rancher/hardened-coredns\n  tag: v1.14.3-build20260511\n",
		map[string]string{v1alpha1.ManagedAnnotationPrefix + "rke2-coredns": "v1.34.1+rke2r1"})

	h.reconcile("rke2-traefik")

	requireCondition(t, h.get("rke2-traefik"), v1alpha1.ConditionBlocked, metav1.ConditionTrue, "StalePatchesExist")
	requireMessage(t, h.get("rke2-traefik"), v1alpha1.ConditionBlocked, `refusing to patch: active patch for component "rke2-coredns" from RKE2 v1.34.1+rke2r1 exists`)
}

// requireMessage checks condition messages that intentionally reuse the CLI wording
func requireMessage(t *testing.T, ip *v1alpha1.ImagePatch, conditionType string, want string) {
	t.Helper()
	condition := meta.FindStatusCondition(ip.Status.Conditions, conditionType)
	if condition == nil || !strings.Contains(condition.Message, want) {
		t.Fatalf("condition %s message = %+v, want it to contain %q", conditionType, condition, want)
	}
}

func TestUpgradeWithAutoRevertRepatchesAgainstNewBaseline(t *testing.T) {
	ip := imagePatch("rke2-traefik", newerTag)
	ip.Spec.UpgradePolicy = v1alpha1.UpgradePolicyAutoRevert
	h := newHarness(t, ip)
	h.reconcile("rke2-traefik")
	h.cluster.images = []string{traefikImage + ":" + newerTag}

	// RKE2 upgrade bundling targetTag
	h.cluster.version = upgradedVersion
	h.cluster.zeroDay = time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	h.cluster.charts["rke2-traefik"] = "image:\n  repository: rancher/hardened-traefik\n  tag: " + targetTag + "\n"
	h.reconcile("rke2-traefik")

	if tag, _ := h.cluster.hccValue(t, "rke2-traefik", "image", "tag"); tag != newerTag {
		t.Fatalf("HelmChartConfig image.tag = %v, want %s", tag, newerTag)
	}
	if version, _ := h.cluster.hccAnnotation(t, "rke2-traefik", managedTraefik); version != upgradedVersion {
		t.Fatalf("managed annotation = %q, want %s", version, upgradedVersion)
	}
	got := h.get("rke2-traefik")
	if got.Status.BaselineTag != targetTag || got.Status.ClusterVersion != upgradedVersion {
		t.Fatalf("unexpected status after AutoRevert: %+v", got.Status)
	}
	requireCondition(t, got, v1alpha1.ConditionStale, metav1.ConditionFalse, "")
}

// After an upgrade the bundled tag can overtake spec.tag (or the chart is replaced only after
// the AutoRevert re-patch): the controller must not keep the cluster on an older image
func TestBundledTagOvertakingSpecTagRevertsPatch(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-traefik", targetTag))
	h.reconcile("rke2-traefik")

	h.cluster.charts["rke2-traefik"] = "image:\n  repository: rancher/hardened-traefik\n  tag: " + newerTag + "\n"
	h.reconcile("rke2-traefik")

	if _, found := h.cluster.hccValue(t, "rke2-traefik", "image", "tag"); found {
		t.Fatal("patch to a tag older than the bundled one was kept")
	}
	requireCondition(t, h.get("rke2-traefik"), v1alpha1.ConditionBlocked, metav1.ConditionTrue, "NotNewer")
}

func TestDeleteRevertsPatchKeepingUserValues(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-traefik", targetTag))
	h.cluster.setHCC("rke2-traefik", "ports:\n  web: 8080\n", nil)
	h.reconcile("rke2-traefik")

	h.delete("rke2-traefik")
	h.reconcile("rke2-traefik")

	h.requireGone("rke2-traefik")
	if _, found := h.cluster.hccValue(t, "rke2-traefik", "image", "tag"); found {
		t.Fatal("delete did not revert the patcher values")
	}
	if _, found := h.cluster.hccAnnotation(t, "rke2-traefik", managedTraefik); found {
		t.Fatal("delete did not remove the managed annotation")
	}
	if port, _ := h.cluster.hccValue(t, "rke2-traefik", "ports", "web"); port != 8080 {
		t.Fatalf("revert dropped the user's values: %v", port)
	}
}

// Status is not needed to revert: ownership is on the HelmChartConfig
func TestDeleteRevertsEvenWithoutStatus(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-traefik", targetTag))
	h.reconcile("rke2-traefik")

	ip := h.get("rke2-traefik")
	ip.Status = v1alpha1.ImagePatchStatus{}
	if err := h.client.Status().Update(context.Background(), ip); err != nil {
		t.Fatal(err)
	}

	h.delete("rke2-traefik")
	h.reconcile("rke2-traefik")
	if _, found := h.cluster.hccValue(t, "rke2-traefik", "image", "tag"); found {
		t.Fatal("delete without status did not revert the patch")
	}
}

func TestSharedChartComponentsAreTrackedIndependently(t *testing.T) {
	h := newHarness(t, imagePatch("rke2-coredns-cluster-autoscaler", "v1.10.2-build20260910"), imagePatch("rke2-coredns", "v1.14.3-build20260511"))
	h.cluster.tags = []string{"v1.14.3-build20260511", "v1.14.2-build20260310", "v1.10.2-build20260910", "v1.10.1-build20260801"}

	h.cluster.images = []string{"rancher/hardened-cluster-autoscaler:v1.10.1-build20260801"}
	h.reconcile("rke2-coredns-cluster-autoscaler")
	h.cluster.images = []string{"rancher/hardened-coredns:v1.14.2-build20260310"}
	h.reconcile("rke2-coredns")

	if tag, _ := h.cluster.hccValue(t, "rke2-coredns", "autoscaler", "image", "tag"); tag != "v1.10.2-build20260910" {
		t.Fatalf("autoscaler tag = %v", tag)
	}
	if tag, _ := h.cluster.hccValue(t, "rke2-coredns", "image", "tag"); tag != "v1.14.3-build20260511" {
		t.Fatalf("coredns tag = %v", tag)
	}

	// Reverting one component leaves the other one's values and annotation alone
	h.delete("rke2-coredns-cluster-autoscaler")
	h.reconcile("rke2-coredns-cluster-autoscaler")
	if _, found := h.cluster.hccValue(t, "rke2-coredns", "autoscaler", "image", "tag"); found {
		t.Fatal("autoscaler values not reverted")
	}
	if tag, _ := h.cluster.hccValue(t, "rke2-coredns", "image", "tag"); tag != "v1.14.3-build20260511" {
		t.Fatalf("coredns values lost when reverting the autoscaler: %v", tag)
	}
	if _, found := h.cluster.hccAnnotation(t, "rke2-coredns", v1alpha1.ManagedAnnotationPrefix+"rke2-coredns"); !found {
		t.Fatal("coredns managed annotation lost when reverting the autoscaler")
	}
}
