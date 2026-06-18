package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/manuelbuil/rke2-patcher/internal/components"
	"github.com/manuelbuil/rke2-patcher/internal/kube"
	"github.com/manuelbuil/rke2-patcher/internal/nodeplan"
)

func resolveOrFail(t *testing.T, name string) components.Component {
	t.Helper()
	c, err := components.Resolve(name)
	if err != nil {
		t.Fatalf("Resolve(%q) error = %v", name, err)
	}
	return c
}

// stubApplyPlan replaces kube.ApplyPlan with a recorder for the duration of the test.
func stubApplyPlan(t *testing.T) *[]string {
	t.Helper()
	applied := []string{}
	original := kube.ApplyPlan
	kube.ApplyPlan = func(content string) error {
		applied = append(applied, content)
		return nil
	}
	t.Cleanup(func() { kube.ApplyPlan = original })
	return &applied
}

func TestRunNodePlan_DryRunDoesNotApply(t *testing.T) {
	applied := stubApplyPlan(t)

	err := runNodePlan(resolveOrFail(t, "rke2-runtime"),
		nodeplan.Options{ImageTag: "v1.31.4-rke2r1"},
		nodePlanOptions{DryRun: true})
	if err != nil {
		t.Fatalf("runNodePlan() error = %v", err)
	}
	if len(*applied) != 0 {
		t.Fatalf("dry-run should not apply Plans, applied %d", len(*applied))
	}
}

func TestRunNodePlan_AutoApproveAppliesServerAndAgent(t *testing.T) {
	applied := stubApplyPlan(t)

	err := runNodePlan(resolveOrFail(t, "rke2-runtime"),
		nodeplan.Options{ImageTag: "v1.31.4-rke2r1"},
		nodePlanOptions{AutoApprove: true})
	if err != nil {
		t.Fatalf("runNodePlan() error = %v", err)
	}
	if len(*applied) != 2 {
		t.Fatalf("runtime should apply server+agent Plans, applied %d", len(*applied))
	}
	if !strings.Contains(applied2(applied), "rke2-runtime-server") ||
		!strings.Contains(applied2(applied), "rke2-runtime-agent") {
		t.Fatalf("expected server and agent Plans, got:\n%s", applied2(applied))
	}
}

func TestRunNodePlan_ServerOnlyAppliesSinglePlan(t *testing.T) {
	applied := stubApplyPlan(t)

	err := runNodePlan(resolveOrFail(t, "rke2-kubernetes"),
		nodeplan.Options{ImageTag: "v1.31.4"},
		nodePlanOptions{AutoApprove: true})
	if err != nil {
		t.Fatalf("runNodePlan() error = %v", err)
	}
	if len(*applied) != 1 {
		t.Fatalf("server-only component should apply 1 Plan, applied %d", len(*applied))
	}
}

func TestRunNodePlan_PromptRejectedSkipsApply(t *testing.T) {
	applied := stubApplyPlan(t)

	originalPrompt := promptYesNoFn
	promptYesNoFn = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { promptYesNoFn = originalPrompt })

	err := runNodePlan(resolveOrFail(t, "rke2-runtime"),
		nodeplan.Options{ImageTag: "v1.31.4-rke2r1"},
		nodePlanOptions{})
	if err != nil {
		t.Fatalf("runNodePlan() error = %v", err)
	}
	if len(*applied) != 0 {
		t.Fatalf("rejected prompt should not apply Plans, applied %d", len(*applied))
	}
}

func TestRunNodePlan_PromptApprovedApplies(t *testing.T) {
	applied := stubApplyPlan(t)

	originalPrompt := promptYesNoFn
	prompted := false
	promptYesNoFn = func(string) (bool, error) { prompted = true; return true, nil }
	t.Cleanup(func() { promptYesNoFn = originalPrompt })

	err := runNodePlan(resolveOrFail(t, "rke2-kubernetes"),
		nodeplan.Options{ImageTag: "v1.31.4"},
		nodePlanOptions{})
	if err != nil {
		t.Fatalf("runNodePlan() error = %v", err)
	}
	if !prompted {
		t.Fatal("expected prompt to be shown when not auto-approving")
	}
	if len(*applied) != 1 {
		t.Fatalf("approved prompt should apply Plan, applied %d", len(*applied))
	}
}

func TestRunNodePlan_RejectsHelmComponent(t *testing.T) {
	applied := stubApplyPlan(t)

	err := runNodePlan(resolveOrFail(t, "rke2-coredns"),
		nodeplan.Options{ImageTag: "1.2.3"},
		nodePlanOptions{AutoApprove: true})
	if err == nil {
		t.Fatal("expected error for Helm-managed component")
	}
	if len(*applied) != 0 {
		t.Fatalf("Helm component should not apply Plans, applied %d", len(*applied))
	}
}

func TestRunNodePlan_PropagatesApplyError(t *testing.T) {
	original := kube.ApplyPlan
	kube.ApplyPlan = func(string) error { return errors.New("boom") }
	t.Cleanup(func() { kube.ApplyPlan = original })

	err := runNodePlan(resolveOrFail(t, "rke2-kubernetes"),
		nodeplan.Options{ImageTag: "v1.31.4"},
		nodePlanOptions{AutoApprove: true})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected apply error to propagate, got %v", err)
	}
}

func TestRunImagePatch_RejectsNodeLevelComponent(t *testing.T) {
	// The guard must fire before any Kubernetes call, so this needs no stubs.
	err := runImagePatch(resolveOrFail(t, "rke2-runtime"), imagePatchOptions{})
	if err == nil {
		t.Fatal("expected image-patch to reject a node-level component")
	}
	if !strings.Contains(err.Error(), "node-plan") {
		t.Fatalf("error should point users to node-plan, got %v", err)
	}
}

// applied2 joins recorded Plan manifests for substring assertions.
func applied2(applied *[]string) string {
	return strings.Join(*applied, "\n---\n")
}
