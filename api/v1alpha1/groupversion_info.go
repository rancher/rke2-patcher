// Package v1alpha1 contains the API types of the rke2-patcher controller.
// +kubebuilder:object:generate=true
// +groupName=patcher.rke2.cattle.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion = schema.GroupVersion{Group: "patcher.rke2.cattle.io", Version: "v1alpha1"}

	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	AddToScheme = SchemeBuilder.AddToScheme
)
