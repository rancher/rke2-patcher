package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// UpgradePolicy decides what happens to a patch after RKE2 is upgraded.
// +kubebuilder:validation:Enum=Manual;AutoRevert
type UpgradePolicy string

const (
	// UpgradePolicyManual leaves the patch in place and marks the ImagePatch Stale.
	// The user reverts it by deleting the ImagePatch.
	UpgradePolicyManual UpgradePolicy = "Manual"
	// UpgradePolicyAutoRevert reverts the patch automatically after an upgrade and then
	// re-evaluates spec.tag against the newly bundled image.
	UpgradePolicyAutoRevert UpgradePolicy = "AutoRevert"
)

const (
	// Finalizer reverts the patch before the ImagePatch is removed.
	Finalizer = "patcher.rke2.cattle.io/revert"
	// ManagedAnnotationPrefix marks the HelmChartConfig values the controller manages: the
	// annotation patcher.rke2.cattle.io/<component> holds the Kubernetes version of the RKE2
	// release the patch was applied on. Values without it are never modified or reverted.
	ManagedAnnotationPrefix = "patcher.rke2.cattle.io/"

	ConditionReady     = "Ready"
	ConditionApplied   = "Applied"
	ConditionRolledOut = "RolledOut"
	ConditionBlocked   = "Blocked"
	ConditionStale     = "Stale"
)

// ImagePatchSpec defines the desired image of an RKE2 component.
type ImagePatchSpec struct {
	// Component is the RKE2 component to patch.
	// +kubebuilder:validation:Enum=rke2-traefik;rke2-ingress-nginx;rke2-coredns;rke2-dns-node-cache;rke2-metrics-server;rke2-flannel;rke2-canal-calico;rke2-canal-flannel;rke2-coredns-cluster-autoscaler;rke2-snapshot-controller
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="component is immutable"
	Component string `json:"component"`

	// Tag is the exact image tag to run. It must be newer than the bundled tag, on the same
	// minor release line and inside the 45-day patch window. Setting it to the bundled tag
	// reverts the patch while keeping the ImagePatch.
	// +kubebuilder:validation:Pattern=`^v[0-9]+\.[0-9]+\.[0-9]+.*$`
	Tag string `json:"tag"`

	// UpgradePolicy decides what happens after an RKE2 upgrade.
	// +kubebuilder:default=Manual
	// +optional
	UpgradePolicy UpgradePolicy `json:"upgradePolicy,omitempty"`
}

// ImagePatchStatus reports what the controller observed. Nothing in it is needed to revert
// a patch: the bundled tag comes from the chart and ownership from the HelmChartConfig.
type ImagePatchStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// ClusterVersion is the Kubernetes version of the RKE2 release the patch was applied on.
	// +optional
	ClusterVersion string `json:"clusterVersion,omitempty"`
	// BaselineTag is the tag bundled with the running RKE2 release (read from its chart).
	// +optional
	BaselineTag string `json:"baselineTag,omitempty"`
	// AppliedTag is the tag currently written to the HelmChartConfig.
	// +optional
	AppliedTag string `json:"appliedTag,omitempty"`
	// RunningImages lists the images the component's pods currently run.
	// +optional
	RunningImages []string `json:"runningImages,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ImagePatch pins an RKE2 component to a specific hardened image tag.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=ip
// +kubebuilder:printcolumn:name="Tag",type=string,JSONPath=`.spec.tag`
// +kubebuilder:printcolumn:name="Applied",type=string,JSONPath=`.status.appliedTag`
// +kubebuilder:printcolumn:name="Baseline",type=string,JSONPath=`.status.baselineTag`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == self.spec.component",message="metadata.name must equal spec.component"
type ImagePatch struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ImagePatchSpec   `json:"spec"`
	Status ImagePatchStatus `json:"status,omitempty"`
}

// ImagePatchList contains a list of ImagePatch.
// +kubebuilder:object:root=true
type ImagePatchList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ImagePatch `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ImagePatch{}, &ImagePatchList{})
}

// ManagedAnnotation is the HelmChartConfig annotation marking a component's values as
// managed by its ImagePatch
func ManagedAnnotation(component string) string {
	return ManagedAnnotationPrefix + component
}
