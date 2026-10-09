// Package state manages the patch state of the CLI mode, stored in the rke2-patcher-state
// ConfigMap. The controller mode does not use it; it only checks that it is empty, because
// the two modes are exclusive.
package state

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
	NamespaceEnv     = "RKE2_PATCHER_CVE_NAMESPACE"
	DefaultNamespace = "rke2-patcher"

	updateAttempts = 5
)

type State struct {
	Entries map[string]Entry `json:"entries"`
}

type Entry struct {
	Component              string `json:"component"`
	ClusterVersion         string `json:"clusterVersion"`
	BaselineTag            string `json:"baselineTag"`
	PatchedToTag           string `json:"patchedToTag"`
	GeneratedValuesContent string `json:"generatedValuesContent,omitempty"`
}

// Store loads and saves the state with optimistic concurrency via a resource version
type Store interface {
	Load(namespace string) (State, string, error)
	Save(namespace string, state State, resourceVersion string) error
}

// StoreFuncs adapts plain functions to a Store
type StoreFuncs struct {
	LoadFn func(namespace string) (State, string, error)
	SaveFn func(namespace string, state State, resourceVersion string) error
}

func (s StoreFuncs) Load(namespace string) (State, string, error) { return s.LoadFn(namespace) }
func (s StoreFuncs) Save(namespace string, state State, resourceVersion string) error {
	return s.SaveFn(namespace, state, resourceVersion)
}

// ConfigMapStore stores the state in the rke2-patcher-state ConfigMap
type ConfigMapStore struct{}

func (ConfigMapStore) Load(namespace string) (State, string, error) {
	state := State{Entries: map[string]Entry{}}

	content, resourceVersion, err := kube.LoadStateConfigMapDataWithResourceVersion(namespace)
	if err != nil {
		return State{}, "", err
	}

	if strings.TrimSpace(content) == "" {
		return state, resourceVersion, nil
	}

	if err := json.Unmarshal([]byte(content), &state); err != nil {
		return State{}, "", fmt.Errorf("failed to parse patch state payload in ConfigMap %s/%s key %q: %w", namespace, kube.StateConfigMapName, kube.StateConfigMapDataKey, err)
	}

	if state.Entries == nil {
		state.Entries = map[string]Entry{}
	}

	return state, resourceVersion, nil
}

func (ConfigMapStore) Save(namespace string, state State, resourceVersion string) error {
	if state.Entries == nil {
		state.Entries = map[string]Entry{}
	}

	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize patch state: %w", err)
	}

	return kube.SaveStateConfigMapDataWithResourceVersion(namespace, string(content), resourceVersion)
}

// Namespace returns the namespace holding the state ConfigMap
func Namespace() string {
	namespace := strings.TrimSpace(os.Getenv(NamespaceEnv))
	if namespace == "" {
		return DefaultNamespace
	}

	return namespace
}

// EntryKey is the state key of a component patched on a given RKE2 version
func EntryKey(clusterVersion string, componentName string) string {
	return clusterVersion + "|" + componentName
}

// EntriesForComponent returns the keys of all entries (any RKE2 version) for a component
func EntriesForComponent(state State, componentName string) []string {
	var keys []string
	for key, entry := range state.Entries {
		if components.SameComponent(entry.Component, componentName) {
			keys = append(keys, key)
		}
	}
	return keys
}

// StaleKeys returns the keys of entries recorded on a different RKE2 version
func StaleKeys(state State, currentVersion string) []string {
	var keys []string
	for key, entry := range state.Entries {
		if strings.TrimSpace(entry.ClusterVersion) != currentVersion {
			keys = append(keys, key)
		}
	}
	return keys
}

// Update loads the state, applies mutate and saves it, retrying on write conflicts.
// If mutate returns changed=false nothing is written.
func Update(store Store, namespace string, mutate func(state *State) (bool, error)) error {
	for attempt := 0; attempt < updateAttempts; attempt++ {
		state, resourceVersion, err := store.Load(namespace)
		if err != nil {
			return err
		}
		if state.Entries == nil {
			state.Entries = map[string]Entry{}
		}

		changed, err := mutate(&state)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}

		err = store.Save(namespace, state, resourceVersion)
		if err == nil {
			return nil
		}

		if k8serrors.IsConflict(err) || k8serrors.IsAlreadyExists(err) {
			continue
		}

		return err
	}

	return fmt.Errorf("failed to update patch state in ConfigMap %s/%s after retries", namespace, kube.StateConfigMapName)
}

// Persist records a patch decision. The first observed baseline tag for an entry is kept.
func Persist(store Store, namespace string, key string, entry Entry) error {
	return Update(store, namespace, func(state *State) (bool, error) {
		if existing, found := state.Entries[key]; found {
			if existing.PatchedToTag == entry.PatchedToTag && existing.BaselineTag == entry.BaselineTag {
				return false, nil
			}

			if strings.TrimSpace(existing.BaselineTag) != "" {
				entry.BaselineTag = existing.BaselineTag
			}
		}

		state.Entries[key] = entry
		return true, nil
	})
}

// RemoveEntries deletes the given keys from the state
func RemoveEntries(store Store, namespace string, keys []string) error {
	return Update(store, namespace, func(state *State) (bool, error) {
		changed := false
		for _, key := range keys {
			if _, found := state.Entries[key]; found {
				delete(state.Entries, key)
				changed = true
			}
		}
		return changed, nil
	})
}
