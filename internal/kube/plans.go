package kube

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// planGVR is the GroupVersionResource for system-upgrade-controller Plans.
var planGVR = schema.GroupVersionResource{Group: "upgrade.cattle.io", Version: "v1", Resource: "plans"}

// ApplyPlan is a variable for testability, delegates to applyPlanImpl.
var ApplyPlan = applyPlanImpl

// applyPlanImpl creates or updates a system-upgrade-controller Plan
// (upgrade.cattle.io/v1) from the provided YAML manifest.
func applyPlanImpl(yamlContent string) error {
	un := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(yamlContent), &un.Object); err != nil {
		return fmt.Errorf("failed to unmarshal Plan YAML: %w", err)
	}

	namespace := un.GetNamespace()
	if namespace == "" {
		namespace = "system-upgrade"
	}

	dynamicClient, err := kubeDynamicClient()
	if err != nil {
		return err
	}

	resource := dynamicClient.Resource(planGVR).Namespace(namespace)
	name := un.GetName()

	// Try to get the existing object
	existing, err := resource.Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// Create if not found
			_, err = resource.Create(context.Background(), un, metav1.CreateOptions{})
			return err
		}
		return err
	}

	// Update if found
	un.SetResourceVersion(existing.GetResourceVersion())
	_, err = resource.Update(context.Background(), un, metav1.UpdateOptions{})
	return err
}
