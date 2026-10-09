package docker

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	v1alpha1 "github.com/rancher/rke2-patcher/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// In controller mode the scenarios' image-patch / image-reconcile calls are translated into
// ImagePatch operations, so every scenario also exercises the controller. Successes and
// refusals are reported like the CLI does: the controller reuses the CLI's messages.

const (
	controllerPatchTimeout  = 6 * time.Minute
	controllerRevertTimeout = 3 * time.Minute
	controllerPollInterval  = 3 * time.Second
)

func (config *TestConfig) controllerDeployed() bool {
	return config.ExecMode == execModeController || config.EnableController
}

// runControllerPatch applies an ImagePatch and waits until the controller reports the outcome
// for this spec: Ready (applied and rolled out), Blocked or Stale
func (config *TestConfig) runControllerPatch(component string, tag string) (string, error) {
	if tag == "" {
		next, output, err := config.nextTag(component)
		if err != nil {
			return output, fmt.Errorf("image-patch failed for %s: %w", component, err)
		}
		tag = next
	}

	upgradePolicy := v1alpha1.UpgradePolicyManual
	if existing, err := config.GetImagePatch(component); err != nil {
		return "", err
	} else if existing != nil && existing.Spec.UpgradePolicy != "" {
		upgradePolicy = existing.Spec.UpgradePolicy
	}
	if err := config.ApplyImagePatch(component, tag, upgradePolicy); err != nil {
		return err.Error(), fmt.Errorf("image-patch failed for %s: %w", component, err)
	}

	deadline := time.Now().Add(controllerPatchTimeout)
	lastMessage := "no status reported"
	for time.Now().Before(deadline) {
		ip, err := config.GetImagePatch(component)
		if err != nil {
			return err.Error(), err
		}

		if ip != nil {
			if ready := currentCondition(ip, v1alpha1.ConditionReady); ready != nil {
				lastMessage = fmt.Sprintf("%s: %s", ready.Reason, ready.Message)
				if ready.Status == metav1.ConditionTrue {
					output := fmt.Sprintf("applied HelmChartConfig for %s through ImagePatch %s (tag %s, %s)", component, ip.Name, tag, ready.Reason)
					fmt.Printf("[docker-tests] controller output=%s\n", output)
					return output, nil
				}
			}
			for _, conditionType := range []string{v1alpha1.ConditionBlocked, v1alpha1.ConditionStale} {
				if condition := currentCondition(ip, conditionType); condition != nil && condition.Status == metav1.ConditionTrue {
					return condition.Message, fmt.Errorf("image-patch failed for %s: ImagePatch %s: %s", component, conditionType, condition.Reason)
				}
			}
		}

		time.Sleep(controllerPollInterval)
	}

	return lastMessage, fmt.Errorf("image-patch failed for %s: ImagePatch not ready after %s (last: %s)", component, controllerPatchTimeout, lastMessage)
}

// runControllerReconcile deletes the component's ImagePatch and waits until the finalizer
// reverted it. Without an ImagePatch it runs the CLI, which works in controller mode too
// (it only reverts patches made with the CLI).
func (config *TestConfig) runControllerReconcile(component string) (string, error) {
	ip, err := config.GetImagePatch(component)
	if err != nil {
		return err.Error(), err
	}
	if ip == nil {
		return config.RunCLIImageReconcile(component, false)
	}

	if err := config.DeleteImagePatch(component); err != nil {
		return err.Error(), err
	}

	deadline := time.Now().Add(controllerRevertTimeout)
	for time.Now().Before(deadline) {
		ip, err := config.GetImagePatch(component)
		if err != nil {
			return err.Error(), err
		}
		if ip == nil {
			return fmt.Sprintf("reverted %s by deleting ImagePatch %s", component, component), nil
		}
		time.Sleep(controllerPollInterval)
	}

	return "", fmt.Errorf("image-reconcile failed for %s: ImagePatch still present after %s", component, controllerRevertTimeout)
}

// currentCondition returns a condition only if it describes the current spec
func currentCondition(ip *v1alpha1.ImagePatch, conditionType string) *metav1.Condition {
	condition := meta.FindStatusCondition(ip.Status.Conditions, conditionType)
	if condition == nil || condition.ObservedGeneration != ip.Generation {
		return nil
	}
	return condition
}

type imageListOutput struct {
	Tags []struct {
		Tag   string `json:"tag"`
		InUse bool   `json:"inUse"`
	} `json:"tags"`
}

// nextTag resolves the tag `image-patch` would pick without --tag: the next newer tag after
// the running one, as listed (newest first) by `image-list --json`
func (config *TestConfig) nextTag(component string) (string, string, error) {
	output, err := config.runPatcherCommand([]string{"image-list", "--json", component})
	if err != nil {
		return "", output, err
	}

	start, end := strings.Index(output, "{"), strings.LastIndex(output, "}")
	if start < 0 || end < start {
		return "", output, fmt.Errorf("image-list returned no JSON: %s", output)
	}
	var listed imageListOutput
	if err := json.Unmarshal([]byte(output[start:end+1]), &listed); err != nil {
		return "", output, fmt.Errorf("failed to parse image-list output: %w", err)
	}

	for index, tag := range listed.Tags {
		if !tag.InUse {
			continue
		}
		if index == 0 {
			message := fmt.Sprintf("refusing to patch: current tag %q is already the latest", tag.Tag)
			return "", message, fmt.Errorf("%s", message)
		}
		return listed.Tags[index-1].Tag, output, nil
	}

	return "", output, fmt.Errorf("image-list reported no tag in use for %s", component)
}
