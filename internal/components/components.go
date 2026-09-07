package components

import (
	"fmt"
	"sort"
	"strings"
)

// Kind describes how a component image is configured in an RKE2 cluster.
type Kind string

const (
	// KindHelm components are managed declaratively via HelmChartConfig
	// (helm.cattle.io/v1) objects. This is the default for every component.
	KindHelm Kind = "helm"
	// KindNode components are core RKE2 images that are NOT deployed via Helm.
	// They are resolved at process startup from node config
	// (/etc/rancher/rke2/config.yaml keys such as runtime-image or
	// kube-apiserver-image) and therefore cannot be changed with a
	// HelmChartConfig. Rolling them out requires editing node config and
	// restarting the rke2 service on each node, which is delegated to the
	// system-upgrade-controller via a generated Plan (see internal/nodeplan).
	KindNode Kind = "node"
)

type Component struct {
	Name                string
	Kind                Kind
	Repository          string
	HelmChartConfigName string
	Workload            WorkloadRef
	// NodeConfigKeys are the rke2 config.yaml keys that select this image for
	// KindNode components. A single hardened image can back several keys (for
	// example hardened-kubernetes backs all four control-plane static pods).
	NodeConfigKeys []string
	// ServerOnly indicates the image is only consumed on control-plane/server
	// nodes (for example the hardened-kubernetes static-pod control plane).
	ServerOnly bool
}

// IsNodeLevel reports whether the component is a core, non-Helm image that is
// configured through node config rather than a HelmChartConfig.
func (c Component) IsNodeLevel() bool {
	return c.Kind == KindNode
}

type WorkloadRef struct {
	Kind      string
	Namespace string
	Name      string
}

var registry = map[string]Component{
	"rke2-traefik": {
		Repository:          "rancher/hardened-traefik",
		HelmChartConfigName: "rke2-traefik",
		Workload: WorkloadRef{
			Kind:      "daemonset",
			Namespace: "kube-system",
			Name:      "rke2-traefik",
		},
	},
	"rke2-ingress-nginx": {
		Repository:          "rancher/nginx-ingress-controller",
		HelmChartConfigName: "rke2-ingress-nginx",
		Workload: WorkloadRef{
			Kind:      "daemonset",
			Namespace: "kube-system",
			Name:      "rke2-ingress-nginx-controller",
		},
	},
	"rke2-coredns": {
		Repository:          "rancher/hardened-coredns",
		HelmChartConfigName: "rke2-coredns",
		Workload: WorkloadRef{
			Kind:      "deployment",
			Namespace: "kube-system",
			Name:      "rke2-coredns-rke2-coredns",
		},
	},
	"rke2-dns-node-cache": {
		Repository:          "rancher/hardened-dns-node-cache",
		HelmChartConfigName: "rke2-coredns",
		Workload: WorkloadRef{
			Kind:      "daemonset",
			Namespace: "kube-system",
			Name:      "node-local-dns",
		},
	},
	"rke2-metrics-server": {
		Repository:          "rancher/hardened-k8s-metrics-server",
		HelmChartConfigName: "rke2-metrics-server",
		Workload: WorkloadRef{
			Kind:      "deployment",
			Namespace: "kube-system",
			Name:      "rke2-metrics-server",
		},
	},
	"rke2-flannel": {
		Repository:          "rancher/hardened-flannel",
		HelmChartConfigName: "rke2-flannel",
		Workload: WorkloadRef{
			Kind:      "daemonset",
			Namespace: "kube-system",
			Name:      "kube-flannel-ds",
		},
	},
	"rke2-canal-calico": {
		Repository:          "rancher/hardened-calico",
		HelmChartConfigName: "rke2-canal",
		Workload: WorkloadRef{
			Kind:      "daemonset",
			Namespace: "kube-system",
			Name:      "rke2-canal",
		},
	},
	"rke2-canal-flannel": {
		Repository:          "rancher/hardened-flannel",
		HelmChartConfigName: "rke2-canal",
		Workload: WorkloadRef{
			Kind:      "daemonset",
			Namespace: "kube-system",
			Name:      "rke2-canal",
		},
	},
	"rke2-coredns-cluster-autoscaler": {
		Repository:          "rancher/hardened-cluster-autoscaler",
		HelmChartConfigName: "rke2-coredns",
		Workload: WorkloadRef{
			Kind:      "deployment",
			Namespace: "kube-system",
			Name:      "rke2-coredns-rke2-coredns-autoscaler",
		},
	},
	"rke2-snapshot-controller": {
		Repository:          "rancher/hardened-snapshot-controller",
		HelmChartConfigName: "rke2-snapshot-controller",
		Workload: WorkloadRef{
			Kind:      "deployment",
			Namespace: "kube-system",
			Name:      "rke2-snapshot-controller",
		},
	},

	// Node-level (non-Helm) components. These are not deployed via Helm and
	// have no HelmChartConfig; they are configured from node config and rolled
	// out through the system-upgrade-controller (see internal/nodeplan).
	"rke2-runtime": {
		Kind:           KindNode,
		Repository:     "rancher/rke2-runtime",
		NodeConfigKeys: []string{"runtime-image"},
		// The runtime image is extracted onto every node (containerd, kubelet,
		// kubectl, crictl, ...), so it applies to both servers and agents.
	},
	"rke2-kubernetes": {
		Kind:       KindNode,
		Repository: "rancher/hardened-kubernetes",
		NodeConfigKeys: []string{
			"kube-apiserver-image",
			"kube-controller-manager-image",
			"kube-proxy-image",
			"kube-scheduler-image",
		},
		// The hardened-kubernetes image backs the static-pod control plane,
		// which only exists on server (control-plane) nodes.
		ServerOnly: true,
		Workload: WorkloadRef{
			Kind:      "pod",
			Namespace: "kube-system",
			Name:      "kube-apiserver",
		},
	},
}

// Resolves the component struct of the chosen component
func Resolve(name string) (Component, error) {
	canonicalName, found := canonicalName(name)
	component, found := registry[canonicalName]
	if !found {
		return Component{}, fmt.Errorf("unsupported component %q", name)
	}

	component.Name = canonicalName
	if component.Kind == "" {
		component.Kind = KindHelm
	}

	return component, nil
}

func Supported() []string {
	items := make([]string, 0, len(registry))
	for name := range registry {
		items = append(items, name)
	}
	sort.Strings(items)

	return items
}

func CLIName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}

	if canonicalName, found := canonicalName(trimmed); found {
		return canonicalName
	}

	return trimmed
}

func SameComponent(a string, b string) bool {
	return strings.EqualFold(CLIName(a), CLIName(b))
}

func canonicalName(name string) (string, bool) {
	normalizedName := strings.ToLower(strings.TrimSpace(name))
	if normalizedName == "" {
		return "", false
	}

	if _, found := registry[normalizedName]; found {
		return normalizedName, true
	}

	return normalizedName, false
}
