package cmd

import (
	"fmt"
	"time"

	"github.com/manuelbuil/rke2-patcher/internal/components"
	"github.com/manuelbuil/rke2-patcher/internal/kube"
)

const (
	patchWindowDays              = 45
	kubeAPIServerNamespace       = "kube-system"
	kubeAPIServerSelector        = "component=kube-apiserver"
	kubeAPIServerImageRepository = "rancher/hardened-kubernetes"
)

var clusterZeroDayResolver = resolveClusterZeroDay

func resolveClusterZeroDay() (time.Time, error) {
	image, err := kube.FindRunningImageByRepository(kubeAPIServerNamespace, kubeAPIServerSelector, kubeAPIServerImageRepository)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to determine kube-apiserver image for patch window: %w", err)
	}

	_, tag := kube.SplitImage(image)
	date, parseOK := buildDateFromTag(tag)
	if !parseOK {
		return time.Time{}, fmt.Errorf("failed to extract build date from kube-apiserver tag %q", tag)
	}

	return date, nil
}

func buildDateFromTag(tag string) (time.Time, bool) {
	parsed, ok := parseComparableTag(tag)
	if !ok || parsed.Build <= 0 {
		return time.Time{}, false
	}

	buildDate := fmt.Sprintf("%08d", parsed.Build)
	date, err := time.Parse("20060102", buildDate)
	if err != nil {
		return time.Time{}, false
	}

	return date.UTC(), true
}

func isPatchWindowExempt(componentName string) bool {
	return components.SameComponent(componentName, "rke2-ingress-nginx")
}

func validatePatchWindowWithZeroDay(componentName string, targetTag string, zeroDay time.Time) error {
	if isPatchWindowExempt(componentName) {
		return nil
	}

	targetDate, ok := buildDateFromTag(targetTag)
	if !ok {
		return fmt.Errorf("refusing to patch: target tag %q does not contain a build date required for the %d-day patch window", targetTag, patchWindowDays)
	}

	deadline := zeroDay.AddDate(0, 0, patchWindowDays)
	if targetDate.After(deadline) {
		return fmt.Errorf(
			"refusing to patch: target tag %q (build date %s) is outside the %d-day window from cluster zero-day %s; upgrade RKE2 to continue patching",
			targetTag,
			targetDate.Format("2006-01-02"),
			patchWindowDays,
			zeroDay.Format("2006-01-02"),
		)
	}

	return nil
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
