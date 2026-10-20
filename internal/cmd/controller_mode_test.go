package cmd

import (
	"strings"
	"testing"

	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/kube"
)

func stubControllerMode(t *testing.T, imagePatches ...string) {
	t.Helper()
	original := imagePatchLister
	imagePatchLister = func() ([]string, error) { return imagePatches, nil }
	t.Cleanup(func() { imagePatchLister = original })

	originalLoad := loadPatchStateFromBackend
	loadPatchStateFromBackend = func(string) (patchState, string, error) {
		t.Fatal("the CLI must not read its state in controller mode")
		return patchState{}, "", nil
	}
	t.Cleanup(func() { loadPatchStateFromBackend = originalLoad })

	originalApply := kube.ApplyHelmChartConfig
	kube.ApplyHelmChartConfig = func(string, string) error {
		t.Fatal("the CLI must not write HelmChartConfigs in controller mode")
		return nil
	}
	t.Cleanup(func() { kube.ApplyHelmChartConfig = originalApply })
}

func TestImagePatchRefusedInControllerMode(t *testing.T) {
	stubControllerMode(t, "rke2-traefik", "rke2-coredns")
	component, _ := components.Resolve("rke2-metrics-server")

	err := runImagePatch(component, imagePatchOptions{TargetTag: "v0.8.1-build20260328", DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "controller mode is in use (ImagePatch objects: rke2-coredns, rke2-traefik)") {
		t.Fatalf("expected controller mode refusal, got: %v", err)
	}
}

// image-reconcile only reverts CLI patches, which is how a cluster moves to the controller mode
func TestImageReconcileAllowedInControllerMode(t *testing.T) {
	original := imagePatchLister
	imagePatchLister = func() ([]string, error) { return []string{"rke2-traefik"}, nil }
	t.Cleanup(func() { imagePatchLister = original })

	originalResolver := clusterVersionResolver
	clusterVersionResolver = func() (string, error) { return "v1.35.2+rke2r1", nil }
	t.Cleanup(func() { clusterVersionResolver = originalResolver })

	originalLoad := loadPatchStateFromBackend
	loadPatchStateFromBackend = func(string) (patchState, string, error) {
		return patchState{Entries: map[string]patchEntry{}}, "1", nil
	}
	t.Cleanup(func() { loadPatchStateFromBackend = originalLoad })

	component, _ := components.Resolve("rke2-metrics-server")
	if err := runReconcile(component, true); err != nil {
		t.Fatalf("image-reconcile must work in controller mode, got: %v", err)
	}
}
