package patcher

import (
	"strings"
	"testing"
)

func TestSubtractPatcherValuesContent_RemovesGeneratedKeysLeavesUserKeys(t *testing.T) {
	existingFileContent := `apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: rke2-traefik
  namespace: kube-system
  labels:
    app.kubernetes.io/managed-by: test-suite
    test.rke2-patcher.io/preserve: "true"
spec:
  failurePolicy: abort
  valuesSecrets:
  - name: my-secret
    path: values
  valuesContent: |-
    image:
      repository: rancher/hardened-traefik
      tag: v3.4.0
    service:
      type: ClusterIP
`

	generatedValuesContent := "image:\n  repository: rancher/hardened-traefik\n  tag: v3.4.0"

	result, err := SubtractPatcherValuesContent(existingFileContent, generatedValuesContent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(result, "repository:") || strings.Contains(result, "tag:") {
		t.Fatalf("expected image block to be removed, got:\n%s", result)
	}

	if !strings.Contains(result, "type: ClusterIP") {
		t.Fatalf("expected user service values to be preserved, got:\n%s", result)
	}

	if !strings.Contains(result, "app.kubernetes.io/managed-by: test-suite") || !strings.Contains(result, "test.rke2-patcher.io/preserve: \"true\"") {
		t.Fatalf("expected labels to be preserved, got:\n%s", result)
	}
	if !strings.Contains(result, "failurePolicy: abort") {
		t.Fatalf("expected failurePolicy to be preserved, got:\n%s", result)
	}
	if !strings.Contains(result, "my-secret") || !strings.Contains(result, "valuesSecrets:") {
		t.Fatalf("expected valuesSecrets to be preserved, got:\n%s", result)
	}
}

func TestSubtractPatcherValuesContent_RemovesAllPatcherKeysLeavesEmptySpec(t *testing.T) {
	existingFileContent := `apiVersion: helm.cattle.io/v1
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

	generatedValuesContent := "image:\n  repository: rancher/hardened-traefik\n  tag: v3.4.0"

	result, err := SubtractPatcherValuesContent(existingFileContent, generatedValuesContent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(result, "repository:") || strings.Contains(result, "tag:") {
		t.Fatalf("expected image block to be removed, got:\n%s", result)
	}

	if !strings.Contains(result, "rke2-traefik") {
		t.Fatalf("expected HCC metadata to remain, got:\n%s", result)
	}
}

func TestSubtractPatcherValuesContent_NoOpWhenGeneratedValuesEmpty(t *testing.T) {
	existingFileContent := `apiVersion: helm.cattle.io/v1
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

	result, err := SubtractPatcherValuesContent(existingFileContent, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result != existingFileContent {
		t.Fatalf("expected no-op, got:\n%s", result)
	}
}

func TestSubtractPatcherValuesContent_PreservesUserSiblingKeys(t *testing.T) {
	existingFileContent := `apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: rke2-traefik
  namespace: kube-system
spec:
  valuesContent: |-
    image:
      repository: rancher/hardened-traefik
      tag: v3.4.0
      pullPolicy: IfNotPresent
`

	generatedValuesContent := "image:\n  repository: rancher/hardened-traefik\n  tag: v3.4.0"

	result, err := SubtractPatcherValuesContent(existingFileContent, generatedValuesContent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "pullPolicy") {
		t.Fatalf("expected user pullPolicy to be preserved, got:\n%s", result)
	}

	if strings.Contains(result, "repository:") || strings.Contains(result, "tag:") {
		t.Fatalf("expected patcher-managed keys to be removed, got:\n%s", result)
	}
}
