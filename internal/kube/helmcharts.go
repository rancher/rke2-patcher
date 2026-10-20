package kube

import (
	"context"
	"fmt"
	"strings"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type HelmChartObject struct {
	Name      string
	Namespace string
	Content   string
}

// ListHelmChartsByIdentity returns the HelmChart with the given name and namespace, as a
// single-element slice, or an empty slice if it does not exist. It is a direct Get rather than
// a List+filter: callers always look up one specific, named chart, and a List would fetch every
// HelmChart in the namespace (including their embedded chart archives) just to discard the rest.
func ListHelmChartsByIdentity(name string, namespace string) ([]HelmChartObject, error) {
	trimmedName := strings.TrimSpace(name)
	trimmedNamespace := strings.TrimSpace(namespace)
	if trimmedName == "" {
		return nil, fmt.Errorf("helmchart name cannot be empty")
	}
	if trimmedNamespace == "" {
		return nil, fmt.Errorf("helmchart namespace cannot be empty")
	}

	dynamicClient, err := kubeDynamicClient()
	if err != nil {
		return nil, err
	}

	gvr := schema.GroupVersionResource{Group: "helm.cattle.io", Version: "v1", Resource: "helmcharts"}
	item, err := dynamicClient.Resource(gvr).Namespace(trimmedNamespace).Get(context.Background(), trimmedName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return []HelmChartObject{}, nil
		}
		return nil, fmt.Errorf("failed to get helmchart %s/%s: %w", trimmedNamespace, trimmedName, err)
	}

	content, err := item.MarshalJSON()
	if err != nil {
		return nil, err
	}

	return []HelmChartObject{{
		Name:      item.GetName(),
		Namespace: item.GetNamespace(),
		Content:   string(content),
	}}, nil
}
