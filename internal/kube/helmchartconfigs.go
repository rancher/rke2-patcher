package kube

import (
	"context"
	"fmt"
	"os"
	"strings"

	helmcontrollerv1 "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	syaml "sigs.k8s.io/yaml"
)

type HelmChartConfigObject struct {
	Name      string
	Namespace string
	Content   string
}

// kubeDynamicClient returns a dynamic.Interface using in-cluster config if available, otherwise falls back to kubeconfig.
func kubeDynamicClient() (dynamic.Interface, error) {
	// Try in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fallback to kubeconfig
		kubeconfigPath := os.Getenv("KUBECONFIG")
		if kubeconfigPath == "" {
			kubeconfigPath = "/etc/rancher/rke2/rke2.yaml"
		}
		if _, statErr := os.Stat(kubeconfigPath); statErr != nil {
			home, homeErr := os.UserHomeDir()
			if homeErr == nil {
				kubeconfigPath = home + "/.kube/config"
			}
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
		}
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize kubernetes dynamic client: %w", err)
	}
	return dynamicClient, nil
}

// GetHelmChartConfigByIdentity is a variable for testability, delegates to getHelmChartConfigByIdentityImpl.
var GetHelmChartConfigByIdentity = getHelmChartConfigByIdentityImpl

// cleanObjectMeta removes server-managed fields from ObjectMeta while preserving user-facing metadata.
// This prevents bloated output and unintended diffs when merging/applying.
// Removed: resourceVersion, uid, generation, creationTimestamp, managedFields, selfLink.
// Preserved: name, namespace, labels, annotations, ownerReferences, finalizers.
func cleanObjectMeta(om *metav1.ObjectMeta) {
	om.ResourceVersion = ""
	om.UID = ""
	om.Generation = 0
	om.CreationTimestamp = metav1.Time{}
	om.DeletionTimestamp = nil
	om.DeletionGracePeriodSeconds = nil
	om.ManagedFields = nil
	om.SelfLink = ""
}

// dropStatusField removes the status field from an unstructured object.
// The status field is server-managed and can cause noisy diffs or apply rejections
// on CRDs with status subresources. We only care about metadata and spec.
func dropStatusField(obj map[string]interface{}) {
	delete(obj, "status")
}

// getHelmChartConfigByIdentityImpl retrieves a HelmChartConfigObject by its name and namespace.
func getHelmChartConfigByIdentityImpl(name string, namespace string) (*HelmChartConfigObject, error) {
	trimmedName := strings.TrimSpace(name)
	trimmedNamespace := strings.TrimSpace(namespace)
	if trimmedName == "" {
		return nil, fmt.Errorf("helmchartconfig name cannot be empty")
	}
	if trimmedNamespace == "" {
		return nil, fmt.Errorf("helmchartconfig namespace cannot be empty")
	}

	dynamicClient, err := kubeDynamicClient()
	if err != nil {
		return nil, err
	}

	gvr := schema.GroupVersionResource{Group: "helm.cattle.io", Version: "v1", Resource: "helmchartconfigs"}
	item, err := dynamicClient.Resource(gvr).Namespace(trimmedNamespace).Get(context.Background(), trimmedName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get helmchartconfig %s/%s: %w", trimmedNamespace, trimmedName, err)
	}

	// Drop the status field which is server-managed and can cause noisy diffs or apply rejections.
	// We only care about metadata and spec for merge/apply operations.
	dropStatusField(item.Object)

	// Marshal the unstructured object to JSON/YAML and unmarshal into the typed struct.
	// This preserves all ObjectMeta fields (labels, annotations, etc.) via the typed struct's
	// metav1.ObjectMeta, unlike a custom reduced struct that only captures name/namespace.
	rawBytes, err := syaml.Marshal(item.Object)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal HelmChartConfig object: %w", err)
	}

	var hcc helmcontrollerv1.HelmChartConfig
	if err := syaml.Unmarshal(rawBytes, &hcc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal HelmChartConfig: %w", err)
	}

	// Strip server-managed ObjectMeta fields while preserving user-facing metadata.
	// Server fields like resourceVersion, uid, generation, creationTimestamp, managedFields
	// should not be included in the merge output.
	cleanObjectMeta(&hcc.ObjectMeta)

	if strings.TrimSpace(hcc.APIVersion) == "" {
		hcc.APIVersion = "helm.cattle.io/v1"
	}
	if strings.TrimSpace(hcc.Kind) == "" {
		hcc.Kind = "HelmChartConfig"
	}

	contentBytes, err := syaml.Marshal(hcc)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize HelmChartConfig: %w", err)
	}

	return &HelmChartConfigObject{
		Name:      item.GetName(),
		Namespace: item.GetNamespace(),
		Content:   string(contentBytes),
	}, nil
}

// ApplyHelmChartConfig is a variable for testability, delegates to applyHelmChartConfigImpl.
var ApplyHelmChartConfig = applyHelmChartConfigImpl

func applyHelmChartConfigImpl(yamlContent string) error {
	un := &unstructured.Unstructured{}
	if err := syaml.Unmarshal([]byte(yamlContent), &un.Object); err != nil {
		return fmt.Errorf("failed to unmarshal HelmChartConfig YAML: %w", err)
	}

	gvr := schema.GroupVersionResource{Group: "helm.cattle.io", Version: "v1", Resource: "helmchartconfigs"}
	namespace := un.GetNamespace()
	if namespace == "" {
		namespace = "kube-system"
	}

	dynamicClient, err := kubeDynamicClient()
	if err != nil {
		return err
	}

	resource := dynamicClient.Resource(gvr).Namespace(namespace)
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
