package state

import (
	"fmt"
	"testing"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// memoryStore fails the first `conflicts` saves to exercise the retry loop
type memoryStore struct {
	state     State
	version   int
	conflicts int
}

func (m *memoryStore) Load(string) (State, string, error) {
	copied := State{Entries: map[string]Entry{}}
	for key, entry := range m.state.Entries {
		copied.Entries[key] = entry
	}
	return copied, fmt.Sprint(m.version), nil
}

func (m *memoryStore) Save(_ string, state State, resourceVersion string) error {
	if m.conflicts > 0 {
		m.conflicts--
		m.version++
		return k8serrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "rke2-patcher-state", fmt.Errorf("conflict"))
	}
	if resourceVersion != fmt.Sprint(m.version) {
		return fmt.Errorf("unexpected resourceVersion %s", resourceVersion)
	}
	m.version++
	m.state = state
	return nil
}

func TestPersistKeepsBaselineAndRetriesConflicts(t *testing.T) {
	store := &memoryStore{conflicts: 2, state: State{Entries: map[string]Entry{
		"v1|rke2-traefik": {Component: "rke2-traefik", ClusterVersion: "v1", BaselineTag: "v1.0.0", PatchedToTag: "v1.0.1"},
	}}}

	err := Persist(store, "ns", "v1|rke2-traefik", Entry{Component: "rke2-traefik", ClusterVersion: "v1", BaselineTag: "v1.0.1", PatchedToTag: "v1.0.2"})
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}

	entry := store.state.Entries["v1|rke2-traefik"]
	if entry.BaselineTag != "v1.0.0" || entry.PatchedToTag != "v1.0.2" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestHelmChartConfigNameForSharedCharts(t *testing.T) {
	cases := map[string]string{
		"rke2-traefik":                    "rke2-traefik",
		"rke2-canal-flannel":              "rke2-canal",
		"rke2-canal-calico":               "rke2-canal",
		"rke2-dns-node-cache":             "rke2-coredns",
		"rke2-coredns-cluster-autoscaler": "rke2-coredns",
	}
	for component, want := range cases {
		if got := HelmChartConfigName(component); got != want {
			t.Errorf("HelmChartConfigName(%q) = %q, want %q", component, got, want)
		}
	}
}
