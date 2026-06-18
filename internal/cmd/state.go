package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/kube"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	patchStateNamespaceEnv     = "RKE2_PATCHER_CVE_NAMESPACE"
	defaultPatchStateNamespace = "rke2-patcher"
)

var (
	loadPatchStateFromBackend = loadPatchStateFromKubernetes
	savePatchStateToBackend   = savePatchStateToKubernetes
	ensureStateNamespace      = kube.EnsureNamespace
)

// generateStateWrite creates a patchStateWrite object representing the intent to patch a component from currentTag to targetTag
func generateStateWrite(componentName string, currentTag string, targetTag string, generatedValuesContent string) (patchStateWrite, error) {
	clusterVersion, err := clusterVersionResolver()
	if err != nil {
		return patchStateWrite{}, fmt.Errorf("failed to resolve cluster version for patch eligibility check: %w", err)
	}

	namespace := patchStateNamespace()
	state, _, err := loadPatchStateFromBackend(namespace)
	if err != nil {
		return patchStateWrite{}, err
	}

	for _, entry := range state.Entries {
		if strings.TrimSpace(entry.ClusterVersion) != clusterVersion {
			componentName := components.CLIName(entry.Component)
			return patchStateWrite{}, fmt.Errorf("refusing to patch: active patch for component %q from RKE2 %s exists; run 'rke2-patcher image-reconcile %s' first", componentName, entry.ClusterVersion, componentName)
		}
	}

	entryKey := clusterVersion + "|" + componentName
	baselineTag := currentTag
	if existing, found := state.Entries[entryKey]; found {
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

// persistPatchDecision attempts to persist the patch decision in the Kubernetes ConfigMap,
// retrying on conflicts to handle concurrent updates
func persistPatchDecision(decision patchStateWrite) error {
	stateNamespace := strings.TrimSpace(decision.StateNamespace)
	if stateNamespace == "" {
		stateNamespace = patchStateNamespace()
	}

	if err := ensureStateNamespace(stateNamespace); err != nil {
		return err
	}

	for attempt := 0; attempt < 5; attempt++ {
		state, resourceVersion, err := loadPatchStateFromBackend(stateNamespace)
		if err != nil {
			return err
		}

		if existing, found := state.Entries[decision.EntryName]; found {
			if existing.PatchedToTag == decision.Entry.PatchedToTag && existing.BaselineTag == decision.Entry.BaselineTag {
				return nil
			}

			if strings.TrimSpace(existing.BaselineTag) != "" {
				decision.Entry.BaselineTag = existing.BaselineTag
			}
		}

		state.Entries[decision.EntryName] = decision.Entry
		err = savePatchStateToBackend(stateNamespace, state, resourceVersion)
		if err == nil {
			return nil
		}

		if k8serrors.IsConflict(err) || k8serrors.IsAlreadyExists(err) {
			continue
		}

		return err
	}

	return fmt.Errorf("failed to persist patch state in ConfigMap %s/%s after retries", stateNamespace, kube.StateConfigMapName)
}

// patchStateNamespace returns the Kubernetes namespace to use for storing patch state, based on the RKE2_PATCHER_CVE_NAMESPACE env var or defaulting to "rke2-patcher"
func patchStateNamespace() string {
	namespace := strings.TrimSpace(os.Getenv(patchStateNamespaceEnv))
	if namespace == "" {
		return defaultPatchStateNamespace
	}

	return namespace
}

// loadPatchStateFromKubernetes loads the rke2-patcher state from the Kubernetes ConfigMap. It returns
// the patch state, the resource version of the ConfigMap for optimistic concurrency control
func loadPatchStateFromKubernetes(namespace string) (patchState, string, error) {
	state := patchState{Entries: map[string]patchEntry{}}

	content, resourceVersion, err := kube.LoadStateConfigMapDataWithResourceVersion(namespace)
	if err != nil {
		return patchState{}, "", err
	}

	if strings.TrimSpace(content) == "" {
		return state, resourceVersion, nil
	}

	if err := json.Unmarshal([]byte(content), &state); err != nil {
		return patchState{}, "", fmt.Errorf("failed to parse patch state payload in ConfigMap %s/%s key %q: %w", namespace, kube.StateConfigMapName, kube.StateConfigMapDataKey, err)
	}

	if state.Entries == nil {
		state.Entries = map[string]patchEntry{}
	}

	return state, resourceVersion, nil
}

// savePatchStateToKubernetes saves the given patch state to the Kubernetes ConfigMap,
// using the provided resource version for optimistic concurrency control
func savePatchStateToKubernetes(namespace string, state patchState, resourceVersion string) error {
	if state.Entries == nil {
		state.Entries = map[string]patchEntry{}
	}

	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize patch state: %w", err)
	}

	if err := kube.SaveStateConfigMapDataWithResourceVersion(namespace, string(content), resourceVersion); err != nil {
		return err
	}

	return nil
}

func staleEntryKeys(state patchState, currentVersion string) []string {
	var keys []string
	for key, entry := range state.Entries {
		if strings.TrimSpace(entry.ClusterVersion) != currentVersion {
			keys = append(keys, key)
		}
	}
	return keys
}

// removeEntriesFromState attempts to remove entries from the patch state in Kubernetes ConfigMap,
// retrying on conflicts to handle concurrent updates
func removeEntriesFromState(namespace string, keysToRemove []string) error {
	for attempt := 0; attempt < 5; attempt++ {
		state, resourceVersion, err := loadPatchStateFromBackend(namespace)
		if err != nil {
			return err
		}

		for _, key := range keysToRemove {
			delete(state.Entries, key)
		}

		err = savePatchStateToBackend(namespace, state, resourceVersion)
		if err == nil {
			return nil
		}

		if k8serrors.IsConflict(err) || k8serrors.IsAlreadyExists(err) {
			continue
		}

		return err
	}

	return fmt.Errorf("failed to remove stale entries from patch state in ConfigMap %s/%s after retries", namespace, kube.StateConfigMapName)
}
