package nodeplan

import (
	"strings"
	"testing"

	"github.com/manuelbuil/rke2-patcher/internal/components"
	"gopkg.in/yaml.v3"
)

func resolveOrFail(t *testing.T, name string) components.Component {
	t.Helper()
	c, err := components.Resolve(name)
	if err != nil {
		t.Fatalf("Resolve(%q) error = %v", name, err)
	}
	return c
}

func TestBuildPlansRuntimeEmitsServerAndAgent(t *testing.T) {
	docs, err := BuildPlans(resolveOrFail(t, "rke2-runtime"), Options{ImageTag: "v1.31.4-rke2r1"})
	if err != nil {
		t.Fatalf("BuildPlans() error = %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 Plans (server+agent), got %d", len(docs))
	}
	if docs[0].Name != "rke2-runtime-server" || docs[1].Name != "rke2-runtime-agent" {
		t.Fatalf("unexpected plan names: %q, %q", docs[0].Name, docs[1].Name)
	}

	var server planDocument
	if err := yaml.Unmarshal([]byte(docs[0].Content), &server); err != nil {
		t.Fatalf("server plan is not valid YAML: %v", err)
	}
	if server.APIVersion != "upgrade.cattle.io/v1" || server.Kind != "Plan" {
		t.Fatalf("unexpected GVK: %s/%s", server.APIVersion, server.Kind)
	}
	if server.Spec.Version != "v1.31.4-rke2r1" {
		t.Fatalf("expected version to track tag, got %q", server.Spec.Version)
	}
	if !server.Spec.Cordon {
		t.Fatal("server plan should cordon")
	}
	if server.Spec.NodeSelector.MatchExpressions[0].Operator != "Exists" {
		t.Fatalf("server plan should select control-plane Exists, got %q", server.Spec.NodeSelector.MatchExpressions[0].Operator)
	}

	var agent planDocument
	if err := yaml.Unmarshal([]byte(docs[1].Content), &agent); err != nil {
		t.Fatalf("agent plan is not valid YAML: %v", err)
	}
	if agent.Spec.NodeSelector.MatchExpressions[0].Operator != "DoesNotExist" {
		t.Fatalf("agent plan should select control-plane DoesNotExist, got %q", agent.Spec.NodeSelector.MatchExpressions[0].Operator)
	}
	if agent.Spec.Prepare == nil || len(agent.Spec.Prepare.Args) != 2 || agent.Spec.Prepare.Args[1] != "rke2-runtime-server" {
		t.Fatalf("agent plan prepare should wait on server plan, got %+v", agent.Spec.Prepare)
	}
	if agent.Spec.Drain == nil || !agent.Spec.Drain.Force {
		t.Fatal("agent plan should drain with force")
	}
}

func TestBuildPlansKubernetesIsServerOnly(t *testing.T) {
	docs, err := BuildPlans(resolveOrFail(t, "rke2-kubernetes"), Options{ImageTag: "v1.31.4"})
	if err != nil {
		t.Fatalf("BuildPlans() error = %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("server-only component should emit 1 Plan, got %d", len(docs))
	}

	var server planDocument
	if err := yaml.Unmarshal([]byte(docs[0].Content), &server); err != nil {
		t.Fatalf("plan is not valid YAML: %v", err)
	}
	keys := ""
	for _, e := range server.Spec.Upgrade.Envs {
		if e.Name == envConfigKeys {
			keys = e.Value
		}
	}
	for _, want := range []string{"kube-apiserver-image", "kube-controller-manager-image", "kube-proxy-image", "kube-scheduler-image"} {
		if !strings.Contains(keys, want) {
			t.Fatalf("expected config keys to include %q, got %q", want, keys)
		}
	}
}

func TestBuildPlansAppliesDefaults(t *testing.T) {
	docs, err := BuildPlans(resolveOrFail(t, "rke2-kubernetes"), Options{ImageTag: "v1"})
	if err != nil {
		t.Fatalf("BuildPlans() error = %v", err)
	}
	if docs[0].Namespace != DefaultNamespace {
		t.Fatalf("expected default namespace %q, got %q", DefaultNamespace, docs[0].Namespace)
	}
	var server planDocument
	_ = yaml.Unmarshal([]byte(docs[0].Content), &server)
	if server.Spec.ServiceAccountName != DefaultServiceAccount {
		t.Fatalf("expected default service account %q, got %q", DefaultServiceAccount, server.Spec.ServiceAccountName)
	}
	if server.Spec.Upgrade.Image != DefaultUpgraderImage {
		t.Fatalf("expected default upgrader image %q, got %q", DefaultUpgraderImage, server.Spec.Upgrade.Image)
	}
	if server.Spec.Concurrency != DefaultConcurrency {
		t.Fatalf("expected default concurrency %d, got %d", DefaultConcurrency, server.Spec.Concurrency)
	}
}

func TestBuildPlansRejectsHelmComponent(t *testing.T) {
	if _, err := BuildPlans(resolveOrFail(t, "rke2-coredns"), Options{ImageTag: "1.2.3"}); err == nil {
		t.Fatal("expected error for Helm-managed component")
	}
}

func TestBuildPlansRequiresTag(t *testing.T) {
	if _, err := BuildPlans(resolveOrFail(t, "rke2-runtime"), Options{}); err == nil {
		t.Fatal("expected error when image tag is empty")
	}
}
