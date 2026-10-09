package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/kube"
	"github.com/rancher/rke2-patcher/internal/state"
)

const patchStateNamespaceEnv = state.NamespaceEnv

var (
	imagePatchLister          = kube.ListImagePatchNames
	loadPatchStateFromBackend = state.ConfigMapStore{}.Load
	savePatchStateToBackend   = state.ConfigMapStore{}.Save
	ensureStateNamespace      = kube.EnsureNamespace
)

// patchStore resolves the backend functions at call time so tests can stub them
func patchStore() state.Store {
	return state.StoreFuncs{
		LoadFn: func(namespace string) (state.State, string, error) { return loadPatchStateFromBackend(namespace) },
		SaveFn: func(namespace string, s state.State, resourceVersion string) error {
			return savePatchStateToBackend(namespace, s, resourceVersion)
		},
	}
}

// generateStateWrite creates a patchStateWrite object representing the intent to patch a component from currentTag to targetTag
func generateStateWrite(componentName string, currentTag string, targetTag string, generatedValuesContent string) (patchStateWrite, error) {
	clusterVersion, err := clusterVersionResolver()
	if err != nil {
		return patchStateWrite{}, fmt.Errorf("failed to resolve cluster version for patch eligibility check: %w", err)
	}

	namespace := patchStateNamespace()
	current, _, err := loadPatchStateFromBackend(namespace)
	if err != nil {
		return patchStateWrite{}, err
	}

	for _, entry := range current.Entries {
		if strings.TrimSpace(entry.ClusterVersion) != clusterVersion {
			componentName := components.CLIName(entry.Component)
			return patchStateWrite{}, fmt.Errorf("refusing to patch: active patch for component %q from RKE2 %s exists; run 'rke2-patcher image-reconcile %s' first", componentName, entry.ClusterVersion, componentName)
		}
	}

	entryKey := state.EntryKey(clusterVersion, componentName)
	baselineTag := currentTag
	if existing, found := current.Entries[entryKey]; found {
		// Keep the first observed baseline tag for this cluster version.
		if strings.TrimSpace(existing.BaselineTag) != "" {
			baselineTag = existing.BaselineTag
		}
	}

	entry := patchEntry{
		Component:              componentName,
		ClusterVersion:         clusterVersion,
		BaselineTag:            baselineTag,
		PatchedToTag:           targetTag,
		GeneratedValuesContent: generatedValuesContent,
	}

	return patchStateWrite{
		StateNamespace: namespace,
		EntryName:      entryKey,
		Entry:          entry,
	}, nil
}

// refuseInControllerMode stops new CLI patches while ImagePatch objects exist: the CLI and
// the controller are exclusive modes. It runs before any other check so the user gets this
// error, not an unrelated one.
func refuseInControllerMode() error {
	names, err := imagePatchLister()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return fmt.Errorf("refusing to run: the ImagePatch controller mode is in use (ImagePatch objects: %s); manage patches through ImagePatch objects, or delete them all to switch back to the CLI (image-reconcile still works)", strings.Join(names, ", "))
}

// persistPatchDecision persists the patch decision in the Kubernetes ConfigMap,
// retrying on conflicts to handle concurrent updates
func persistPatchDecision(decision patchStateWrite) error {
	stateNamespace := strings.TrimSpace(decision.StateNamespace)
	if stateNamespace == "" {
		stateNamespace = patchStateNamespace()
	}

	if err := ensureStateNamespace(stateNamespace); err != nil {
		return err
	}

	return state.Persist(patchStore(), stateNamespace, decision.EntryName, decision.Entry)
}

// patchStateNamespace returns the Kubernetes namespace to use for storing patch state, based on the RKE2_PATCHER_CVE_NAMESPACE env var or defaulting to "rke2-patcher"
func patchStateNamespace() string {
	return state.Namespace()
}

func staleEntryKeys(s patchState, currentVersion string) []string {
	return state.StaleKeys(s, currentVersion)
}

// removeEntriesFromState removes entries from the patch state in Kubernetes ConfigMap,
// retrying on conflicts to handle concurrent updates
func removeEntriesFromState(namespace string, keysToRemove []string) error {
	return state.RemoveEntries(patchStore(), namespace, keysToRemove)
}
