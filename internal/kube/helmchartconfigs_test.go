package kube

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	syaml "sigs.k8s.io/yaml"
)

func TestCleanObjectMeta_StripsServerManagedFields(t *testing.T) {
	// This test verifies that server-managed fields are stripped while user metadata is preserved.

	// Build an ObjectMeta with both server-managed and user-facing fields
	om := metav1.ObjectMeta{
		Name:              "rke2-traefik",
		Namespace:         "kube-system",
		UID:               "16b556f6-500b-46fc-a332-3ff4cf2563d2",
		Generation:        4,
		CreationTimestamp: metav1.Now(),
		ResourceVersion:   "12120",
		SelfLink:          "/apis/helm.cattle.io/v1/namespaces/kube-system/helmchartconfigs/rke2-traefik",
		Labels: map[string]string{
			"app.kubernetes.io/managed-by":  "test-suite",
			"test.rke2-patcher.io/preserve": "true",
		},
		Annotations: map[string]string{
			"description": "Traefik ingress controller",
		},
	}

	// Before cleaning, verify server fields are present
	if om.ResourceVersion == "" {
		t.Fatal("setup error: ResourceVersion should be set before clean")
	}
	if om.UID == "" {
		t.Fatal("setup error: UID should be set before clean")
	}

	// Clean
	cleanObjectMeta(&om)

	// Verify server-managed fields are stripped
	if om.ResourceVersion != "" {
		t.Errorf("expected ResourceVersion to be stripped, got: %q", om.ResourceVersion)
	}
	if om.UID != "" {
		t.Errorf("expected UID to be stripped, got: %q", om.UID)
	}
	if om.Generation != 0 {
		t.Errorf("expected Generation to be stripped, got: %d", om.Generation)
	}
	if om.SelfLink != "" {
		t.Errorf("expected SelfLink to be stripped, got: %q", om.SelfLink)
	}
	if !om.CreationTimestamp.IsZero() {
		t.Errorf("expected CreationTimestamp to be zero, got: %v", om.CreationTimestamp)
	}

	// Verify user-facing metadata is preserved
	if om.Name != "rke2-traefik" {
		t.Errorf("expected Name to be preserved, got: %q", om.Name)
	}
	if om.Namespace != "kube-system" {
		t.Errorf("expected Namespace to be preserved, got: %q", om.Namespace)
	}
	if len(om.Labels) == 0 {
		t.Fatal("expected Labels to be preserved")
	}
	if om.Labels["app.kubernetes.io/managed-by"] != "test-suite" {
		t.Errorf("expected label to be preserved")
	}
	if len(om.Annotations) == 0 {
		t.Fatal("expected Annotations to be preserved")
	}
	if om.Annotations["description"] != "Traefik ingress controller" {
		t.Errorf("expected annotation to be preserved")
	}
}

func TestCleanObjectMeta_OutputDoesNotContainServerFields(t *testing.T) {
	// Verify that when we marshal an ObjectMeta after cleaning,
	// server fields don't appear in the YAML output.

	om := metav1.ObjectMeta{
		Name:              "rke2-traefik",
		Namespace:         "kube-system",
		UID:               "16b556f6-500b-46fc-a332-3ff4cf2563d2",
		Generation:        4,
		CreationTimestamp: metav1.Now(),
		ResourceVersion:   "12120",
		SelfLink:          "/apis/helm.cattle.io/v1/namespaces/kube-system/helmchartconfigs/rke2-traefik",
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": "test-suite",
		},
	}

	cleanObjectMeta(&om)

	// Marshal to YAML
	b, err := syaml.Marshal(om)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	content := string(b)

	// Verify server fields are absent
	serverFields := []string{"uid:", "resourceVersion:", "generation:", "creationTimestamp:", "selfLink:", "managedFields:"}
	for _, field := range serverFields {
		if strings.Contains(content, field) {
			t.Errorf("expected %s to be absent from output, got:\n%s", field, content)
		}
	}

	// Verify user fields are present
	if !strings.Contains(content, "app.kubernetes.io/managed-by") {
		t.Errorf("expected labels to be present in output, got:\n%s", content)
	}
	if !strings.Contains(content, "rke2-traefik") {
		t.Errorf("expected name to be present in output, got:\n%s", content)
	}
}

func TestDropStatusField_RemovesStatusButPreservesOtherFields(t *testing.T) {
	// This test verifies that dropStatusField removes the status field while preserving
	// metadata and spec.

	obj := map[string]interface{}{
		"apiVersion": "helm.cattle.io/v1",
		"kind":       "HelmChartConfig",
		"metadata": map[string]interface{}{
			"name":      "rke2-traefik",
			"namespace": "kube-system",
			"labels": map[string]interface{}{
				"app.kubernetes.io/managed-by": "test-suite",
			},
		},
		"spec": map[string]interface{}{
			"chart": "traefik",
		},
		"status": map[string]interface{}{
			"observedGeneration": float64(4),
			"conditions": []interface{}{
				map[string]interface{}{
					"type":   "Ready",
					"status": "True",
				},
			},
		},
	}

	// Before dropping, verify status is present
	if _, exists := obj["status"]; !exists {
		t.Fatal("setup error: status should be present before drop")
	}

	// Drop
	dropStatusField(obj)

	// Verify status is removed
	if _, exists := obj["status"]; exists {
		t.Errorf("expected status to be dropped")
	}

	// Verify other fields are preserved
	if obj["apiVersion"] != "helm.cattle.io/v1" {
		t.Errorf("expected apiVersion to be preserved")
	}
	if obj["kind"] != "HelmChartConfig" {
		t.Errorf("expected kind to be preserved")
	}

	metadata, ok := obj["metadata"].(map[string]interface{})
	if !ok || metadata["name"] != "rke2-traefik" {
		t.Errorf("expected metadata to be preserved")
	}

	spec, ok := obj["spec"].(map[string]interface{})
	if !ok || spec["chart"] != "traefik" {
		t.Errorf("expected spec to be preserved")
	}
}

func TestDropStatusField_NoErrorIfStatusAbsent(t *testing.T) {
	// This test verifies that dropStatusField safely handles objects without a status field.

	obj := map[string]interface{}{
		"apiVersion": "helm.cattle.io/v1",
		"kind":       "HelmChartConfig",
		"metadata": map[string]interface{}{
			"name": "rke2-traefik",
		},
		"spec": map[string]interface{}{
			"chart": "traefik",
		},
	}

	// Should not panic or error
	dropStatusField(obj)

	// Verify object is unchanged
	if obj["apiVersion"] != "helm.cattle.io/v1" {
		t.Errorf("expected apiVersion to be preserved")
	}
	if obj["kind"] != "HelmChartConfig" {
		t.Errorf("expected kind to be preserved")
	}
}
