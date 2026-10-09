package cmd

import (
	"time"

	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/kube"
	"github.com/rancher/rke2-patcher/internal/policy"
)

var clusterZeroDayResolver = kube.ClusterZeroDay

func buildDateFromTag(tag string) (time.Time, bool) {
	return policy.BuildDate(tag)
}

func isPatchWindowExempt(componentName string) bool {
	return policy.IsPatchWindowExempt(components.CLIName(componentName))
}

func validatePatchWindowWithZeroDay(componentName string, targetTag string, zeroDay time.Time) error {
	return policy.ValidatePatchWindow(components.CLIName(componentName), targetTag, zeroDay)
}

func validatePatchWindow(componentName string, targetTag string) error {
	if isPatchWindowExempt(componentName) {
		return nil
	}

	zeroDay, err := clusterZeroDayResolver()
	if err != nil {
		return err
	}

	return validatePatchWindowWithZeroDay(componentName, targetTag, zeroDay)
}

func splitTagsByPatchWindow(componentName string, tags []string, currentTag string, previousTag string) ([]string, []string, error) {
	if len(tags) == 0 {
		return nil, nil, nil
	}

	if isPatchWindowExempt(componentName) {
		eligible := append([]string(nil), tags...)
		return eligible, nil, nil
	}

	zeroDay, err := clusterZeroDayResolver()
	if err != nil {
		return nil, nil, err
	}

	eligible := make([]string, 0, len(tags))
	blocked := make([]string, 0, len(tags))
	for _, tagName := range tags {
		if tagName == currentTag || tagName == previousTag {
			eligible = append(eligible, tagName)
			continue
		}

		if err := validatePatchWindowWithZeroDay(componentName, tagName, zeroDay); err != nil {
			blocked = append(blocked, tagName)
			continue
		}

		eligible = append(eligible, tagName)
	}

	return eligible, blocked, nil
}
