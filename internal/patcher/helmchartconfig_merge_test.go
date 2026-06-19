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
spec:
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
