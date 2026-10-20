package docker

import (
	"encoding/json"
	"fmt"
	"strings"

	v1alpha1 "github.com/rancher/rke2-patcher/api/v1alpha1"
	"github.com/rancher/rke2-patcher/internal/clistate"
	"github.com/rancher/rke2-patcher/internal/kube"
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

	var imgPatch v1alpha1.ImagePatch
	if err := json.Unmarshal([]byte(out), &imgPatch); err != nil {
		return nil, fmt.Errorf("failed to parse ImagePatch %s: %w: %s", name, err, out)
	}
	return &imgPatch, nil
}

// DeleteImagePatch requests deletion without waiting for the finalizer
func (config *TestConfig) DeleteImagePatch(name string) error {
	if out, err := config.Server.RunKubectl(fmt.Sprintf("delete imagepatch %s --wait=false", name)); err != nil {
		return fmt.Errorf("failed to delete ImagePatch %s: %s: %w", name, out, err)
	}
	return nil
}

// GetPatchState returns the CLI patch state stored in the rke2-patcher-state ConfigMap
func (config *TestConfig) GetPatchState() (clistate.State, error) {
	empty := clistate.State{Entries: map[string]clistate.Entry{}}

	out, err := config.Server.RunKubectl(fmt.Sprintf("-n %s get configmap %s --ignore-not-found -o json", clistate.Namespace(), kube.StateConfigMapName))
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

	var s clistate.State
	if err := json.Unmarshal([]byte(payload), &s); err != nil {
		return empty, fmt.Errorf("failed to parse patch state payload: %w", err)
	}
	if s.Entries == nil {
		s.Entries = map[string]clistate.Entry{}
	}
	return s, nil
}

// PatchStateEntryFor returns the state entry of a component, if any
func (config *TestConfig) PatchStateEntryFor(component string) (clistate.Entry, bool, error) {
	s, err := config.GetPatchState()
	if err != nil {
		return clistate.Entry{}, false, err
	}
	for _, key := range clistate.EntriesForComponent(s, component) {
		return s.Entries[key], true, nil
	}
	return clistate.Entry{}, false, nil
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
