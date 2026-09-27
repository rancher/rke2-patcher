// Package nodeplan renders system-upgrade-controller Plan manifests
// (upgrade.cattle.io/v1) used to roll out node-level RKE2 images that are not
// managed by Helm (for example rke2-runtime and hardened-kubernetes).
//
// Unlike Helm add-ons, these images are resolved at rke2 startup from node
// config (/etc/rancher/rke2/config.yaml) and require restarting the rke2
// service on each node. The generated Plan instructs the
// system-upgrade-controller to run an "upgrader" image on the selected nodes;
// that image writes a config drop-in and restarts rke2. The desired image is
// passed to the upgrader through environment variables on the Plan.
package nodeplan

import (
	"fmt"
	"strings"

	"github.com/manuelbuil/rke2-patcher/internal/components"
	"gopkg.in/yaml.v3"
)

const (
	// DefaultNamespace is the namespace the system-upgrade-controller and its
	// Plans conventionally live in.
	DefaultNamespace = "system-upgrade"
	// DefaultServiceAccount is the ServiceAccount the upgrade Job runs as.
	DefaultServiceAccount = "system-upgrade"
	// DefaultUpgraderImage is the image that applies the change on each node.
	DefaultUpgraderImage = "ghcr.io/rancher/rke2-runtime-upgrader:latest"
	// DefaultConcurrency upgrades one node at a time by default to preserve
	// control-plane (etcd) quorum and minimize disruption.
	DefaultConcurrency = 1

	controlPlaneRoleLabel = "node-role.kubernetes.io/control-plane"

	envImageRepository = "RKE2_UPGRADE_IMAGE_REPOSITORY"
	envImageTag        = "RKE2_UPGRADE_IMAGE_TAG"
	envConfigKeys      = "RKE2_UPGRADE_CONFIG_KEYS"
)

// Options controls how Plans are rendered.
type Options struct {
	Namespace      string
	ServiceAccount string
	UpgraderImage  string
	ImageTag       string
	Concurrency    int
}

func (o Options) withDefaults() Options {
	if strings.TrimSpace(o.Namespace) == "" {
		o.Namespace = DefaultNamespace
	}
	if strings.TrimSpace(o.ServiceAccount) == "" {
		o.ServiceAccount = DefaultServiceAccount
	}
	if strings.TrimSpace(o.UpgraderImage) == "" {
		o.UpgraderImage = DefaultUpgraderImage
	}
	if o.Concurrency <= 0 {
		o.Concurrency = DefaultConcurrency
	}
	return o
}

// Document is a rendered Plan manifest plus its identity.
type Document struct {
	Name      string
	Namespace string
	Content   string
}

type planDocument struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       string     `yaml:"kind"`
	Metadata   objectMeta `yaml:"metadata"`
	Spec       planSpec   `yaml:"spec"`
}

type objectMeta struct {
	Name      string            `yaml:"name"`
	Namespace string            `yaml:"namespace"`
	Labels    map[string]string `yaml:"labels,omitempty"`
}

type planSpec struct {
	Concurrency        int            `yaml:"concurrency"`
	Version            string         `yaml:"version"`
	ServiceAccountName string         `yaml:"serviceAccountName"`
	NodeSelector       nodeSelector   `yaml:"nodeSelector"`
	Cordon             bool           `yaml:"cordon,omitempty"`
	Drain              *drainSpec     `yaml:"drain,omitempty"`
	Prepare            *containerSpec `yaml:"prepare,omitempty"`
	Upgrade            containerSpec  `yaml:"upgrade"`
}

type nodeSelector struct {
	MatchExpressions []matchExpression `yaml:"matchExpressions"`
}

type matchExpression struct {
	Key      string   `yaml:"key"`
	Operator string   `yaml:"operator"`
	Values   []string `yaml:"values,omitempty"`
}

type drainSpec struct {
	Force                    bool `yaml:"force"`
	SkipWaitForDeleteTimeout int  `yaml:"skipWaitForDeleteTimeout,omitempty"`
}

type containerSpec struct {
	Image string   `yaml:"image"`
	Args  []string `yaml:"args,omitempty"`
	Envs  []envVar `yaml:"envs,omitempty"`
}

type envVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

// BuildPlans renders the Plan manifest(s) needed to roll a node-level image out
// across the cluster. Server (control-plane) nodes are always upgraded first;
// for images that also run on agents, a second Plan is emitted whose prepare
// step waits for the server Plan to finish so the control plane leads.
func BuildPlans(component components.Component, opts Options) ([]Document, error) {
	if !component.IsNodeLevel() {
		return nil, fmt.Errorf("component %q is not a node-level component", component.Name)
	}
	if len(component.NodeConfigKeys) == 0 {
		return nil, fmt.Errorf("component %q has no node config keys", component.Name)
	}

	opts = opts.withDefaults()
	tag := strings.TrimSpace(opts.ImageTag)
	if tag == "" {
		return nil, fmt.Errorf("image tag is required")
	}

	envs := []envVar{
		{Name: envImageRepository, Value: component.Repository},
		{Name: envImageTag, Value: tag},
		{Name: envConfigKeys, Value: strings.Join(component.NodeConfigKeys, ",")},
	}

	serverName := fmt.Sprintf("%s-server", component.Name)
	server := planDocument{
		APIVersion: "upgrade.cattle.io/v1",
		Kind:       "Plan",
		Metadata: objectMeta{
			Name:      serverName,
			Namespace: opts.Namespace,
			Labels:    map[string]string{"rke2-patcher.io/component": component.Name},
		},
		Spec: planSpec{
			Concurrency:        opts.Concurrency,
			Version:            tag,
			ServiceAccountName: opts.ServiceAccount,
			Cordon:             true,
			NodeSelector: nodeSelector{
				MatchExpressions: []matchExpression{
					{Key: controlPlaneRoleLabel, Operator: "Exists"},
				},
			},
			Upgrade: containerSpec{Image: opts.UpgraderImage, Envs: envs},
		},
	}

	serverDoc, err := render(server)
	if err != nil {
		return nil, err
	}
	docs := []Document{serverDoc}

	if component.ServerOnly {
		return docs, nil
	}

	agent := planDocument{
		APIVersion: "upgrade.cattle.io/v1",
		Kind:       "Plan",
		Metadata: objectMeta{
			Name:      fmt.Sprintf("%s-agent", component.Name),
			Namespace: opts.Namespace,
			Labels:    map[string]string{"rke2-patcher.io/component": component.Name},
		},
		Spec: planSpec{
			Concurrency:        opts.Concurrency,
			Version:            tag,
			ServiceAccountName: opts.ServiceAccount,
			NodeSelector: nodeSelector{
				MatchExpressions: []matchExpression{
					{Key: controlPlaneRoleLabel, Operator: "DoesNotExist"},
				},
			},
			Drain: &drainSpec{Force: true, SkipWaitForDeleteTimeout: 60},
			// Wait for the server Plan to finish before touching agents so the
			// control plane is upgraded first.
			Prepare: &containerSpec{
				Image: opts.UpgraderImage,
				Args:  []string{"prepare", serverName},
			},
			Upgrade: containerSpec{Image: opts.UpgraderImage, Envs: envs},
		},
	}

	agentDoc, err := render(agent)
	if err != nil {
		return nil, err
	}

	return append(docs, agentDoc), nil
}

func render(p planDocument) (Document, error) {
	content, err := yaml.Marshal(p)
	if err != nil {
		return Document{}, fmt.Errorf("failed to render Plan %q: %w", p.Metadata.Name, err)
	}
	return Document{
		Name:      p.Metadata.Name,
		Namespace: p.Metadata.Namespace,
		Content:   string(content),
	}, nil
}
