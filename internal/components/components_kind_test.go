package components

import "testing"

func TestResolveDefaultsKindToHelm(t *testing.T) {
	c, err := Resolve("rke2-coredns")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if c.Kind != KindHelm {
		t.Fatalf("expected KindHelm, got %q", c.Kind)
	}
	if c.IsNodeLevel() {
		t.Fatal("Helm component should not be node-level")
	}
}

func TestResolveNodeLevelComponents(t *testing.T) {
	cases := map[string]struct {
		serverOnly bool
		keyCount   int
	}{
		"rke2-runtime":    {serverOnly: false, keyCount: 1},
		"rke2-kubernetes": {serverOnly: true, keyCount: 4},
	}
	for name, want := range cases {
		c, err := Resolve(name)
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", name, err)
		}
		if !c.IsNodeLevel() {
			t.Fatalf("%q should be node-level", name)
		}
		if c.ServerOnly != want.serverOnly {
			t.Fatalf("%q ServerOnly = %v, want %v", name, c.ServerOnly, want.serverOnly)
		}
		if len(c.NodeConfigKeys) != want.keyCount {
			t.Fatalf("%q NodeConfigKeys = %d, want %d", name, len(c.NodeConfigKeys), want.keyCount)
		}
	}
}
