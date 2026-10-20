package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rancher/rke2-patcher/internal/clistate"
	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/kube"
)

const patchStateNamespaceEnv = clistate.NamespaceEnv

var (
	imagePatchLister          = kube.ListImagePatchNames
	loadPatchStateFromBackend = clistate.ConfigMapStore{}.Load
	savePatchStateToBackend   = clistate.ConfigMapStore{}.Save
	ensureStateNamespace      = kube.EnsureNamespace
)

// patchStore resolves the backend functions at call time so tests can stub them
func patchStore() clistate.Store {
	return clistate.StoreFuncs{
		LoadFn: func(namespace string) (clistate.State, string, error) { return loadPatchStateFromBackend(namespace) },
		SaveFn: func(namespace string, s clistate.State, resourceVersion string) error {
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

	entryKey := clistate.EntryKey(clusterVersion, componentName)
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

	return clistate.Persist(patchStore(), stateNamespace, decision.EntryName, decision.Entry)
}

// patchStateNamespace returns the Kubernetes namespace to use for storing patch state, based on the RKE2_PATCHER_CVE_NAMESPACE env var or defaulting to "rke2-patcher"
func patchStateNamespace() string {
	return clistate.Namespace()
}

func staleEntryKeys(s patchState, currentVersion string) []string {
	return clistate.StaleKeys(s, currentVersion)
}

// removeEntriesFromState removes entries from the patch state in Kubernetes ConfigMap,
// retrying on conflicts to handle concurrent updates
func removeEntriesFromState(namespace string, keysToRemove []string) error {
	return clistate.RemoveEntries(patchStore(), namespace, keysToRemove)
}
