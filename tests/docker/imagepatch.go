package docker

import (
	"encoding/json"
	"fmt"
	"strings"

	v1alpha1 "github.com/rancher/rke2-patcher/api/v1alpha1"
	"github.com/rancher/rke2-patcher/internal/kube"
	"github.com/rancher/rke2-patcher/internal/state"
	corev1 "k8s.io/api/core/v1"
)

// ApplyImagePatch creates or updates the ImagePatch for a component
func (config *TestConfig) ApplyImagePatch(component string, tag string, upgradePolicy v1alpha1.UpgradePolicy) error {
	return config.ApplyImagePatchNamed(component, component, tag, upgradePolicy)
}

// ApplyImagePatchNamed allows a name different from the component, to exercise validation
func (config *TestConfig) ApplyImagePatchNamed(name string, component string, tag string, upgradePolicy v1alpha1.UpgradePolicy) error {
	manifest := fmt.Sprintf(`---
apiVersion: patcher.rke2.cattle.io/v1alpha1
kind: ImagePatch
metadata:
  name: %s
spec:
  component: %s
  tag: %s
  upgradePolicy: %s
`, name, component, tag, upgradePolicy)
	return config.ApplyManifest(manifest)
}

// GetImagePatch returns the ImagePatch, or nil if it does not exist
func (config *TestConfig) GetImagePatch(name string) (*v1alpha1.ImagePatch, error) {
	out, err := config.Server.RunKubectl(fmt.Sprintf("get imagepatch %s --ignore-not-found -o json", name))
	if err != nil {
		return nil, fmt.Errorf("failed to get ImagePatch %s: %s: %w", name, out, err)
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}

	var ip v1alpha1.ImagePatch
	if err := json.Unmarshal([]byte(out), &ip); err != nil {
		return nil, fmt.Errorf("failed to parse ImagePatch %s: %w: %s", name, err, out)
	}
	return &ip, nil
}

// DeleteImagePatch requests deletion without waiting for the finalizer
func (config *TestConfig) DeleteImagePatch(name string) error {
	if out, err := config.Server.RunKubectl(fmt.Sprintf("delete imagepatch %s --wait=false", name)); err != nil {
		return fmt.Errorf("failed to delete ImagePatch %s: %s: %w", name, out, err)
	}
	return nil
}

// AnnotateImagePatch sets an annotation on the ImagePatch
func (config *TestConfig) AnnotateImagePatch(name string, key string, value string) error {
	if out, err := config.Server.RunKubectl(fmt.Sprintf("annotate imagepatch %s %s=%s --overwrite", name, key, value)); err != nil {
		return fmt.Errorf("failed to annotate ImagePatch %s: %s: %w", name, out, err)
	}
	return nil
}

// GetPatchState returns the CLI patch state stored in the rke2-patcher-state ConfigMap
func (config *TestConfig) GetPatchState() (state.State, error) {
	empty := state.State{Entries: map[string]state.Entry{}}

	out, err := config.Server.RunKubectl(fmt.Sprintf("-n %s get configmap %s --ignore-not-found -o json", patcherNamespace, kube.StateConfigMapName))
	if err != nil {
		return empty, fmt.Errorf("failed to get patch state: %s: %w", out, err)
	}
	if strings.TrimSpace(out) == "" {
		return empty, nil
	}

	var configMap corev1.ConfigMap
	if err := json.Unmarshal([]byte(out), &configMap); err != nil {
		return empty, fmt.Errorf("failed to parse patch state ConfigMap: %w", err)
	}

	payload := strings.TrimSpace(configMap.Data[kube.StateConfigMapDataKey])
	if payload == "" {
		return empty, nil
	}

	var s state.State
	if err := json.Unmarshal([]byte(payload), &s); err != nil {
		return empty, fmt.Errorf("failed to parse patch state payload: %w", err)
	}
	if s.Entries == nil {
		s.Entries = map[string]state.Entry{}
	}
	return s, nil
}

// PatchStateEntryFor returns the state entry of a component, if any
func (config *TestConfig) PatchStateEntryFor(component string) (state.Entry, bool, error) {
	s, err := config.GetPatchState()
	if err != nil {
		return state.Entry{}, false, err
	}
	for _, key := range state.EntriesForComponent(s, component) {
		return s.Entries[key], true, nil
	}
	return state.Entry{}, false, nil
}

// SetHelmChartConfigValues overwrites the valuesContent of a HelmChartConfig, e.g. to simulate drift
func (config *TestConfig) SetHelmChartConfigValues(name string, valuesContent string) error {
	indented := ""
	for _, line := range strings.Split(strings.TrimRight(valuesContent, "\n"), "\n") {
		indented += "    " + line + "\n"
	}
	manifest := fmt.Sprintf(`---
apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: %s
  namespace: kube-system
spec:
  valuesContent: |-
%s`, name, indented)
	return config.ApplyManifest(manifest)
}

// ControllerLogs returns the last lines of the controller logs
func (config *TestConfig) ControllerLogs(lines int) string {
	out, err := config.Server.RunKubectl(fmt.Sprintf("-n %s logs deployment/%s --tail=%d", patcherNamespace, patcherReleaseName, lines))
	if err != nil {
		return fmt.Sprintf("failed to get controller logs: %v: %s", err, out)
	}
	return out
}

// ImagePatchEvents returns the events recorded for ImagePatch objects
func (config *TestConfig) ImagePatchEvents() string {
	out, err := config.Server.RunKubectl("get events -A --field-selector involvedObject.kind=ImagePatch --sort-by=.lastTimestamp")
	if err != nil {
		return fmt.Sprintf("failed to get events: %v: %s", err, out)
	}
	return out
}
