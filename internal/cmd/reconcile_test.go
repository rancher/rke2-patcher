package cmd

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/kube"
	cli "github.com/urfave/cli/v2"
)

func TestEvaluatePatchEligibility_BlocksWhenStaleVersionExists(t *testing.T) {
	t.Setenv(patchStateNamespaceEnv, "")

	originalResolver := clusterVersionResolver
	clusterVersionResolver = func() (string, error) {
		return "v1.35.2+rke2r1", nil
	}
	t.Cleanup(func() { clusterVersionResolver = originalResolver })

	originalLoad := loadPatchStateFromBackend
	loadPatchStateFromBackend = func(namespace string) (patchState, string, error) {
		state := patchState{
			Entries: map[string]patchEntry{
				"v1.35.1+rke2r1|rke2-traefik": {
					Component:      "rke2-traefik",
					ClusterVersion: "v1.35.1+rke2r1",
					BaselineTag:    "v3.6.7",
					PatchedToTag:   "v3.6.9",
				},
			},
		}
		return state, "1", nil
	}
	t.Cleanup(func() { loadPatchStateFromBackend = originalLoad })

	_, err := generateStateWrite("traefik", "v3.6.9", "v3.6.10", "")
	if err == nil {
		t.Fatalf("expected error when stale version entry exists, got nil")
	}

	if !strings.Contains(err.Error(), "reconcile") {
		t.Fatalf("expected error to mention reconcile, got: %v", err)
	}

	if !strings.Contains(err.Error(), "reconcile rke2-traefik") {
		t.Fatalf("expected error to suggest component-scoped reconcile, got: %v", err)
	}

	if !strings.Contains(err.Error(), "v1.35.1+rke2r1") {
		t.Fatalf("expected error to mention stale version, got: %v", err)
	}
}

func TestEvaluatePatchEligibility_AllowsWhenAllEntriesMatchCurrentVersion(t *testing.T) {
	t.Setenv(patchStateNamespaceEnv, "")

	originalResolver := clusterVersionResolver
	clusterVersionResolver = func() (string, error) {
		return "v1.35.2+rke2r1", nil
	}
	t.Cleanup(func() { clusterVersionResolver = originalResolver })

	originalLoad := loadPatchStateFromBackend
	loadPatchStateFromBackend = func(namespace string) (patchState, string, error) {
		state := patchState{
			Entries: map[string]patchEntry{
				"v1.35.2+rke2r1|flannel": {
					Component:      "flannel",
					ClusterVersion: "v1.35.2+rke2r1",
					BaselineTag:    "v0.26.0",
					PatchedToTag:   "v0.26.1",
				},
			},
		}
		return state, "1", nil
	}
	t.Cleanup(func() { loadPatchStateFromBackend = originalLoad })

	decision, err := generateStateWrite("traefik", "v3.6.9", "v3.6.10", "")
	if err != nil {
		t.Fatalf("expected patch to be allowed when no stale-version entries exist, got: %v", err)
	}

	if decision.EntryName == "" {
		t.Fatalf("expected decision to include entry key")
	}
}

func TestStaleEntryKeys_ReturnsOnlyDifferentVersionKeys(t *testing.T) {
	state := patchState{
		Entries: map[string]patchEntry{
			"v1.35.1+rke2r1|traefik": {ClusterVersion: "v1.35.1+rke2r1"},
			"v1.35.2+rke2r1|flannel": {ClusterVersion: "v1.35.2+rke2r1"},
			"v1.35.1+rke2r1|calico":  {ClusterVersion: "v1.35.1+rke2r1"},
		},
	}

	keys := staleEntryKeys(state, "v1.35.2+rke2r1")

	if len(keys) != 2 {
		t.Fatalf("expected 2 stale keys, got %d: %v", len(keys), keys)
	}

	for _, k := range keys {
		if strings.Contains(k, "v1.35.2+rke2r1") {
			t.Fatalf("expected only v1.35.1 entries to be stale, got: %s", k)
		}
	}
}

func TestReconcileEntry_StripsGeneratedValuesFromFile(t *testing.T) {

	// In-memory mock for HelmChartConfig state
	var (
		helmChartConfigContent = `apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: rke2-traefik
  namespace: kube-system
spec:
  valuesContent: |-
    image:
      repository: rancher/hardened-traefik
      tag: v3.4.0
    service:
      type: ClusterIP
`
		appliedContent string
	)

	// Mock kube API
	originalGet := kube.GetHelmChartConfigByIdentity
	originalApply := kube.ApplyHelmChartConfig
	defer func() {
		kube.GetHelmChartConfigByIdentity = originalGet
		kube.ApplyHelmChartConfig = originalApply
	}()

	kube.GetHelmChartConfigByIdentity = func(name, namespace string) (*kube.HelmChartConfigObject, error) {
		return &kube.HelmChartConfigObject{
			Content: helmChartConfigContent,
		}, nil
	}
	kube.ApplyHelmChartConfig = func(content string) error {
		appliedContent = content
		return nil
	}

	entry := patchEntry{
		Component:              "traefik",
		ClusterVersion:         "v1.35.1+rke2r1",
		BaselineTag:            "v3.3.0",
		PatchedToTag:           "v3.4.0",
		GeneratedValuesContent: "image:\n  repository: rancher/hardened-traefik\n  tag: v3.4.0",
	}

	reconciled, err := reconcileEntry(entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reconciled {
		t.Fatalf("expected reconcileEntry to report successful reconciliation")
	}

	if strings.Contains(appliedContent, "repository:") || strings.Contains(appliedContent, "tag:") {
		t.Fatalf("expected patcher image keys to be removed, got:\n%s", appliedContent)
	}
	if !strings.Contains(appliedContent, "type: ClusterIP") {
		t.Fatalf("expected user service values to be preserved, got:\n%s", appliedContent)
	}
}

func TestReconcileEntry_NoOpWhenFileDoesNotExist(t *testing.T) {

	entry := patchEntry{
		Component:              "traefik",
		ClusterVersion:         "v1.35.1+rke2r1",
		GeneratedValuesContent: "image:\n  repository: rancher/hardened-traefik\n  tag: v3.4.0",
	}

	// Mock cluster returns nothing (no HelmChartConfig found)
	originalGet := kube.GetHelmChartConfigByIdentity
	defer func() { kube.GetHelmChartConfigByIdentity = originalGet }()
	kube.GetHelmChartConfigByIdentity = func(name, namespace string) (*kube.HelmChartConfigObject, error) {
		return nil, nil
	}

	reconciled, err := reconcileEntry(entry)
	if err != nil {
		t.Fatalf("unexpected error when config does not exist: %v", err)
	}
	if reconciled {
		t.Fatalf("expected missing-config entry to be skipped")
	}

	reconciled, err = reconcileEntry(entry)
	if err != nil {
		t.Fatalf("unexpected error when file does not exist: %v", err)
	}
	if reconciled {
		t.Fatalf("expected missing-file entry to be skipped")
	}
}

func TestRunReconcile_DoesNotRemoveStateWhenFileIsMissing(t *testing.T) {
	useInMemoryPatchStateBackend(t)

	originalGet := kube.GetHelmChartConfigByIdentity
	originalApply := kube.ApplyHelmChartConfig
	kube.GetHelmChartConfigByIdentity = func(name, namespace string) (*kube.HelmChartConfigObject, error) {
		// Return nil to simulate the "file is missing" scenario
		return nil, nil
	}
	kube.ApplyHelmChartConfig = func(content string) error {
		return nil
	}
	t.Cleanup(func() {
		kube.GetHelmChartConfigByIdentity = originalGet
		kube.ApplyHelmChartConfig = originalApply
	})

	originalResolver := clusterVersionResolver
	clusterVersionResolver = func() (string, error) {
		return "v1.35.2+rke2r1", nil
	}
	t.Cleanup(func() {
		clusterVersionResolver = originalResolver
	})

	traefikComponent, err := components.Resolve("rke2-traefik")
	if err != nil {
		t.Fatalf("failed to resolve traefik component: %v", err)
	}

	if err := persistPatchDecision(patchStateWrite{
		StateNamespace: patchStateNamespace(),
		EntryName:      "v1.35.1+rke2r1|" + traefikComponent.Name,
		Entry: patchEntry{
			Component:              traefikComponent.Name,
			ClusterVersion:         "v1.35.1+rke2r1",
			BaselineTag:            "v3.3.0",
			PatchedToTag:           "v3.4.0",
			GeneratedValuesContent: "image:\n  repository: rancher/hardened-traefik\n  tag: v3.4.0",
		},
	}); err != nil {
		t.Fatalf("failed to persist traefik state: %v", err)
	}

	if err := runReconcile(traefikComponent, false); err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}

	state, _, err := loadPatchStateFromBackend(patchStateNamespace())
	if err != nil {
		t.Fatalf("failed to load state: %v", err)
	}
	if _, found := state.Entries["v1.35.1+rke2r1|"+traefikComponent.Name]; !found {
		t.Fatalf("expected stale state entry to remain when reconcile file is missing")
	}
}

func TestRunReconcile_OnlyTouchesTargetComponent(t *testing.T) {
	useInMemoryPatchStateBackend(t)

	var originalGet = kube.GetHelmChartConfigByIdentity
	var originalApply = kube.ApplyHelmChartConfig
	kube.GetHelmChartConfigByIdentity = func(name, namespace string) (*kube.HelmChartConfigObject, error) {
		// Return nil to simulate the "file is missing" scenario
		return nil, nil
	}
	kube.ApplyHelmChartConfig = func(content string) error {
		return nil
	}
	t.Cleanup(func() {
		kube.GetHelmChartConfigByIdentity = originalGet
		kube.ApplyHelmChartConfig = originalApply
	})

	originalResolver := clusterVersionResolver
	clusterVersionResolver = func() (string, error) {
		return "v1.35.2+rke2r1", nil
	}
	t.Cleanup(func() {
		clusterVersionResolver = originalResolver
	})

	// In-memory mock state for HelmChartConfigs
	var (
		traefikContent = `apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
    name: rke2-traefik
    namespace: kube-system
spec:
    valuesContent: |-
        image:
            repository: rancher/hardened-traefik
            tag: v3.4.0
        service:
            type: ClusterIP
`
		flannelContent = `apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
    name: rke2-flannel
    namespace: kube-system
spec:
    valuesContent: |-
        image:
            repository: rancher/hardened-flannel
            tag: v1.0.0
        feature:
            enabled: true
`
		applied = map[string]string{}
	)

	// Mock kube API
	originalGet = kube.GetHelmChartConfigByIdentity
	originalApply = kube.ApplyHelmChartConfig
	defer func() {
		kube.GetHelmChartConfigByIdentity = originalGet
		kube.ApplyHelmChartConfig = originalApply
	}()
	kube.GetHelmChartConfigByIdentity = func(name, namespace string) (*kube.HelmChartConfigObject, error) {
		if name == "rke2-traefik" {
			return &kube.HelmChartConfigObject{Content: traefikContent}, nil
		}
		if name == "rke2-flannel" {
			return &kube.HelmChartConfigObject{Content: flannelContent}, nil
		}
		return nil, nil
	}
	kube.ApplyHelmChartConfig = func(content string) error {
		if strings.Contains(content, "rke2-traefik") {
			applied["traefik"] = content
		}
		if strings.Contains(content, "rke2-flannel") {
			applied["flannel"] = content
		}
		return nil
	}

	traefikComponent, err := components.Resolve("rke2-traefik")
	if err != nil {
		t.Fatalf("failed to resolve traefik component: %v", err)
	}

	flannelComponent, err := components.Resolve("rke2-flannel")
	if err != nil {
		t.Fatalf("failed to resolve flannel component: %v", err)
	}

	if err := persistPatchDecision(patchStateWrite{
		StateNamespace: patchStateNamespace(),
		EntryName:      "v1.35.1+rke2r1|" + traefikComponent.Name,
		Entry: patchEntry{
			Component:              traefikComponent.Name,
			ClusterVersion:         "v1.35.1+rke2r1",
			BaselineTag:            "v3.3.0",
			PatchedToTag:           "v3.4.0",
			GeneratedValuesContent: "image:\n  repository: rancher/hardened-traefik\n  tag: v3.4.0",
		},
	}); err != nil {
		t.Fatalf("failed to persist traefik state: %v", err)
	}

	if err := persistPatchDecision(patchStateWrite{
		StateNamespace: patchStateNamespace(),
		EntryName:      "v1.35.1+rke2r1|" + flannelComponent.Name,
		Entry: patchEntry{
			Component:              flannelComponent.Name,
			ClusterVersion:         "v1.35.1+rke2r1",
			BaselineTag:            "v0.9.0",
			PatchedToTag:           "v1.0.0",
			GeneratedValuesContent: "image:\n  repository: rancher/hardened-flannel\n  tag: v1.0.0",
		},
	}); err != nil {
		t.Fatalf("failed to persist flannel state: %v", err)
	}

	if err := runReconcile(traefikComponent, false); err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}

	// Assert traefik patcher keys are removed
	traefikResult, ok := applied["traefik"]
	if !ok {
		t.Fatalf("expected traefik to be reconciled and applied")
	}
	if strings.Contains(traefikResult, "repository: rancher/hardened-traefik") || strings.Contains(traefikResult, "tag: v3.4.0") {
		t.Fatalf("expected traefik patcher keys to be removed, got:\n%s", traefikResult)
	}
	// Assert flannel file remains untouched (not applied)
	if _, ok := applied["flannel"]; ok {
		t.Fatalf("expected flannel file to remain untouched, but was applied")
	}

	state, _, err := loadPatchStateFromBackend(patchStateNamespace())
	if err != nil {
		t.Fatalf("failed to load state: %v", err)
	}
	if _, found := state.Entries["v1.35.1+rke2r1|"+traefikComponent.Name]; found {
		t.Fatalf("expected traefik stale state entry to be removed")
	}
	if _, found := state.Entries["v1.35.1+rke2r1|"+flannelComponent.Name]; !found {
		t.Fatalf("expected flannel stale state entry to remain")
	}
}

func TestRunReconcileCommand_RequiresComponent(t *testing.T) {
	app := BuildCLIApp()
	set := flag.NewFlagSet("reconcile", flag.ContinueOnError)

	if err := set.Parse([]string{}); err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}

	ctx := cli.NewContext(app, set, nil)
	err := runReconcileCommand(ctx)
	if err == nil {
		t.Fatalf("expected validation error, got nil")
	}

	if !strings.Contains(err.Error(), "component is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunReconcile_PromptsAndKeepsCurrentVersionPatchWhenRejected(t *testing.T) {
	useInMemoryPatchStateBackend(t)

	originalResolver := clusterVersionResolver
	clusterVersionResolver = func() (string, error) {
		return "v1.35.2+rke2r1", nil
	}
	t.Cleanup(func() {
		clusterVersionResolver = originalResolver
	})

	originalPrompt := promptYesNoFn
	promptYesNoFn = func(prompt string) (bool, error) {
		return false, nil
	}
	t.Cleanup(func() {
		promptYesNoFn = originalPrompt
	})

	tempDir := t.TempDir()
	traefikFilePath := filepath.Join(tempDir, "rke2-traefik-config-rke2-patcher.yaml")

	traefikContent := `apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: rke2-traefik
  namespace: kube-system
spec:
  valuesContent: |-
    image:
      repository: rancher/hardened-traefik
      tag: v3.4.0
    service:
      type: ClusterIP
`
	if err := os.WriteFile(traefikFilePath, []byte(traefikContent), 0644); err != nil {
		t.Fatalf("failed to write traefik file: %v", err)
	}

	traefikComponent, err := components.Resolve("rke2-traefik")
	if err != nil {
		t.Fatalf("failed to resolve traefik component: %v", err)
	}

	if err := persistPatchDecision(patchStateWrite{
		StateNamespace: patchStateNamespace(),
		EntryName:      "v1.35.2+rke2r1|" + traefikComponent.Name,
		Entry: patchEntry{
			Component:              traefikComponent.Name,
			ClusterVersion:         "v1.35.2+rke2r1",
			BaselineTag:            "v3.3.0",
			PatchedToTag:           "v3.4.0",
			GeneratedValuesContent: "image:\n  repository: rancher/hardened-traefik\n  tag: v3.4.0",
		},
	}); err != nil {
		t.Fatalf("failed to persist traefik state: %v", err)
	}

	if err := runReconcile(traefikComponent, false); err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}

	updatedTraefik, err := os.ReadFile(traefikFilePath)
	if err != nil {
		t.Fatalf("failed to read traefik file: %v", err)
	}
	if !strings.Contains(string(updatedTraefik), "repository: rancher/hardened-traefik") || !strings.Contains(string(updatedTraefik), "tag: v3.4.0") {
		t.Fatalf("expected traefik file to stay unchanged, got:\n%s", string(updatedTraefik))
	}

	state, _, err := loadPatchStateFromBackend(patchStateNamespace())
	if err != nil {
		t.Fatalf("failed to load state: %v", err)
	}
	if _, found := state.Entries["v1.35.2+rke2r1|"+traefikComponent.Name]; !found {
		t.Fatalf("expected current-version traefik state entry to remain after rejecting revert")
	}
}
