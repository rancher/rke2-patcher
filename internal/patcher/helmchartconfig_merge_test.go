package patcher

import (
	"strings"
	"testing"
)

func TestMergeHelmChartConfigWithContents(t *testing.T) {
	existingContent := `apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: rke2-traefik
  namespace: kube-system
  labels:
    app.kubernetes.io/managed-by: test-suite
    test.rke2-patcher.io/preserve: "true"
  annotations:
    description: "Traefik ingress controller configuration"
    updated-by: "user"
spec:
  failurePolicy: abort
  valuesContent: |-
    service:
      type: ClusterIP
`

	generatedContent := `apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: rke2-traefik
  namespace: kube-system
spec:
  valuesContent: |-
    image:
      repository: rancher/hardened-traefik
      tag: new-tag
`

	merged, err := MergeHelmChartConfigWithContent(generatedContent, existingContent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(merged, "type: ClusterIP") {
		t.Fatalf("expected merged content to include existing values content: %s", merged)
	}
	if !strings.Contains(merged, "repository: rancher/hardened-traefik") || !strings.Contains(merged, "tag: new-tag") {
		t.Fatalf("expected merged content to include generated image values: %s", merged)
	}
	if !strings.Contains(merged, "app.kubernetes.io/managed-by: test-suite") || !strings.Contains(merged, "test.rke2-patcher.io/preserve: \"true\"") {
		t.Fatalf("expected merged content to preserve labels: %s", merged)
	}
	if !strings.Contains(merged, "failurePolicy: abort") {
		t.Fatalf("expected merged content to preserve failurePolicy: %s", merged)
	}
	if !strings.Contains(merged, "description:") || !strings.Contains(merged, "Traefik ingress controller configuration") || !strings.Contains(merged, "updated-by:") || !strings.Contains(merged, "user") {
		t.Fatalf("expected merged content to preserve annotations: %s", merged)
	}
}

func TestHelmChartConfigIdentityFromContent(t *testing.T) {
	content := `apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: rke2-traefik
  namespace: kube-system
spec:
  valuesContent: |-
    image:
      repository: rancher/hardened-traefik
      tag: v3.4.0
`

	name, namespace, err := HelmChartConfigIdentityFromContent(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if name != "rke2-traefik" || namespace != "kube-system" {
		t.Fatalf("unexpected identity: %s/%s", namespace, name)
	}
}

func TestMergeHelmChartConfigWithContents_IndentationPreserved(t *testing.T) {

	existingContent := `apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
    name: rke2-traefik
    namespace: kube-system
spec:
    valuesContent: |-
        providers:
            kubernetesGateway:
                enabled: true
`

	generatedContent := `apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
    name: rke2-traefik
    namespace: kube-system
spec:
    valuesContent: |-
        image: # change made by rke2-patcher
            repository: rancher/hardened-traefik # change made by rke2-patcher
            tag: v3.6.12-build20260409 # change made by rke2-patcher
`

	merged, err := MergeHelmChartConfigWithContent(generatedContent, existingContent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that both blocks are present and properly indented (4 spaces)
	wantImageBlock := "    image:"
	wantProvidersBlock := "    providers:"
	wantKubeGatewayBlock := "      kubernetesGateway:"
	wantEnabledBlock := "        enabled: true"

	if !strings.Contains(merged, wantImageBlock) {
		t.Errorf("expected merged content to contain indented image block: %q\nMerged:\n%s", wantImageBlock, merged)
	}
	if !strings.Contains(merged, wantProvidersBlock) {
		t.Errorf("expected merged content to contain indented providers block: %q\nMerged:\n%s", wantProvidersBlock, merged)
	}
	if !strings.Contains(merged, wantKubeGatewayBlock) {
		t.Errorf("expected merged content to contain indented kubernetesGateway block: %q\nMerged:\n%s", wantKubeGatewayBlock, merged)
	}
	if !strings.Contains(merged, wantEnabledBlock) {
		t.Errorf("expected merged content to contain indented enabled block: %q\nMerged:\n%s", wantEnabledBlock, merged)
	}

	// Check that there are no top-level keys without indentation (should not start at column 0)
	lines := strings.Split(merged, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "image:") || strings.HasPrefix(line, "providers:") {
			t.Errorf("found unindented top-level key in merged valuesContent: %q\nMerged:\n%s", line, merged)
		}
	}
}

func TestMergeValuesContent_ExistingEmpty_NormalizesIncomingIndentation(t *testing.T) {
	incoming := "image:\n  repository: rancher/hardened-traefik\n  tag: new-tag"

	merged, err := mergeValuesContent("", incoming)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(merged, "    image:") {
		t.Fatalf("expected normalized indentation for top-level key, got:\n%s", merged)
	}
	if strings.HasPrefix(merged, "image:") {
		t.Fatalf("expected content to be indented, got:\n%s", merged)
	}
}

func TestMergeValuesContent_ExistingEmpty_ProperlyIndentedPreservedAsIs(t *testing.T) {
	incoming := "    image:\n      repository: rancher/hardened-traefik\n      tag: new-tag"

	merged, err := mergeValuesContent("", incoming)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if merged != incoming {
		t.Fatalf("expected already-indented content to be preserved as-is\nwant:\n%s\ngot:\n%s", incoming, merged)
	}
}

func TestMergeValuesContent_IncomingEmpty_NormalizesExistingIndentation(t *testing.T) {
	existing := "providers:\n  kubernetesGateway:\n    enabled: true"

	merged, err := mergeValuesContent(existing, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(merged, "    providers:") {
		t.Fatalf("expected normalized indentation for top-level key, got:\n%s", merged)
	}
	if strings.HasPrefix(merged, "providers:") {
		t.Fatalf("expected content to be indented, got:\n%s", merged)
	}
}

func TestMergeHelmChartConfigWithContents_NoExistingMatch_NormalizesGenerated(t *testing.T) {
	generatedContent := "apiVersion: helm.cattle.io/v1\n" +
		"kind: HelmChartConfig\n" +
		"metadata:\n" +
		"  name: rke2-traefik\n" +
		"  namespace: kube-system\n" +
		"spec:\n" +
		"  valuesContent: |-\n" +
		"    image:\n" +
		"      repository: rancher/hardened-traefik\n" +
		"      tag: new-tag\n"

	unemptyContent := "apiVersion: helm.cattle.io/v1\n" +
		"kind: HelmChartConfig\n" +
		"metadata:\n" +
		"  name: rke2-traefik\n" +
		"  namespace: kube-system\n" +
		"spec:\n" +
		"  valuesContent: |-\n" +
		"    service:\n" +
		"      type: ClusterIP\n"

	merged, err := MergeHelmChartConfigWithContent(generatedContent, unemptyContent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(merged, "repository: rancher/hardened-traefik") {
		t.Fatalf("expected generated image repository in output, got:\n%s", merged)
	}

	// Verify proper indentation: valuesContent block should be indented 4 spaces
	if !strings.Contains(merged, "    image:") {
		t.Fatalf("expected indented image key in valuesContent block, got:\n%s", merged)
	}
	if strings.Contains(merged, "\nimage:") {
		t.Fatalf("expected image key to be indented under valuesContent, not at line start, got:\n%s", merged)
	}
}

func TestMergeHelmChartConfigWithContents_AfterReconcileEmptyValuesContent_ProducesIndentedValuesBlock(t *testing.T) {
	generatedContent, _ := BuildHelmChartConfig(
		"rke2-coredns",
		"rke2-coredns",
		"registry.rancher.com/rancher/hardened-coredns",
		"v1.14.3-build20260604",
	)

	existingAfterReconcile := `apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: rke2-coredns
  namespace: kube-system
spec:
  failurePolicy: reinstall
  valuesContent: ""
`

	merged, err := MergeHelmChartConfigWithContent(generatedContent, existingAfterReconcile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(merged, "\nimage: # change made by rke2-patcher") {
		t.Fatalf("expected image key to remain indented under valuesContent, got:\n%s", merged)
	}

	if !strings.Contains(merged, "valuesContent: |-\n    image: # change made by rke2-patcher") {
		t.Fatalf("expected valuesContent block to include correctly indented image key, got:\n%s", merged)
	}

}
