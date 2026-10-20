package kube

import (
	"context"
	"fmt"
	"strings"
	"testing"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

var helmChartConfigGVR = schema.GroupVersionResource{Group: "helm.cattle.io", Version: "v1", Resource: "helmchartconfigs"}

func newFakeDynamicClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		helmChartConfigGVR: "HelmChartConfigList",
	}, objects...)
}

func unstructuredHCC(name string, namespace string, resourceVersion string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("helm.cattle.io/v1")
	u.SetKind("HelmChartConfig")
	u.SetName(name)
	u.SetNamespace(namespace)
	u.SetResourceVersion(resourceVersion)
	return u
}

const hccYAML = `apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: rke2-traefik
  namespace: kube-system
spec:
  valuesContent: |-
    image:
      tag: %s
`

// The fake dynamic client's tracker does not itself enforce optimistic concurrency (Update
// always overwrites), so the conflict/already-exists paths are exercised with a reactor that
// simulates what a real apiserver returns, and we assert applyHelmChartConfigWithClient wraps
// it correctly. The non-conflict paths run against the plain tracker.

func TestApplyHelmChartConfigWithClient_CreatesWhenNoResourceVersion(t *testing.T) {
	client := newFakeDynamicClient()

	if err := applyHelmChartConfigWithClient(client, fmt.Sprintf(hccYAML, "v1"), ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	obj, err := client.Resource(helmChartConfigGVR).Namespace("kube-system").Get(context.Background(), "rke2-traefik", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected the object to be created: %v", err)
	}
	valuesContent, _, _ := unstructured.NestedString(obj.Object, "spec", "valuesContent")
	if !strings.Contains(valuesContent, "v1") {
		t.Fatalf("expected the created object to hold the given values, got:\n%s", valuesContent)
	}
}

func TestApplyHelmChartConfigWithClient_CreateAlreadyExistsIsWrapped(t *testing.T) {
	client := newFakeDynamicClient()
	client.PrependReactor("create", "helmchartconfigs", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewAlreadyExists(helmChartConfigGVR.GroupResource(), "rke2-traefik")
	})

	err := applyHelmChartConfigWithClient(client, fmt.Sprintf(hccYAML, "v1"), "")
	if err == nil || !k8serrors.IsAlreadyExists(err) {
		t.Fatalf("expected the wrapped error to still satisfy IsAlreadyExists, got: %v", err)
	}
	if !strings.Contains(err.Error(), "was created concurrently") {
		t.Fatalf("expected a 'created concurrently' message, got: %v", err)
	}
}

func TestApplyHelmChartConfigWithClient_UpdateSendsTheCallerResourceVersion(t *testing.T) {
	client := newFakeDynamicClient(unstructuredHCC("rke2-traefik", "kube-system", "99")) // tracker's own version, ignored below
	var sentResourceVersion string
	client.PrependReactor("update", "helmchartconfigs", func(action clienttesting.Action) (bool, runtime.Object, error) {
		sentResourceVersion = action.(clienttesting.UpdateAction).GetObject().(*unstructured.Unstructured).GetResourceVersion()
		return false, nil, nil // not handled: fall through to the default tracker-backed reactor
	})

	if err := applyHelmChartConfigWithClient(client, fmt.Sprintf(hccYAML, "v2"), "42"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sentResourceVersion != "42" {
		t.Fatalf("Update was called with resourceVersion %q, want the caller's %q", sentResourceVersion, "42")
	}

	obj, err := client.Resource(helmChartConfigGVR).Namespace("kube-system").Get(context.Background(), "rke2-traefik", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	valuesContent, _, _ := unstructured.NestedString(obj.Object, "spec", "valuesContent")
	if !strings.Contains(valuesContent, "v2") {
		t.Fatalf("expected the update to be applied, got:\n%s", valuesContent)
	}
}

func TestApplyHelmChartConfigWithClient_UpdateConflictIsWrapped(t *testing.T) {
	client := newFakeDynamicClient(unstructuredHCC("rke2-traefik", "kube-system", "7"))
	client.PrependReactor("update", "helmchartconfigs", func(clienttesting.Action) (bool, runtime.Object, error) {
		// Simulates the real apiserver rejecting an update against a resourceVersion
		// that is no longer current (another writer updated the object first).
		return true, nil, k8serrors.NewConflict(helmChartConfigGVR.GroupResource(), "rke2-traefik", fmt.Errorf("stale resourceVersion"))
	})

	err := applyHelmChartConfigWithClient(client, fmt.Sprintf(hccYAML, "v2"), "3")
	if err == nil || !k8serrors.IsConflict(err) {
		t.Fatalf("expected the wrapped error to still satisfy IsConflict, got: %v", err)
	}
	if !strings.Contains(err.Error(), "was modified concurrently") {
		t.Fatalf("expected a 'modified concurrently' message, got: %v", err)
	}
}
