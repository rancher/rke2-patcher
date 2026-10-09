package policy

import (
	"fmt"
	"strings"
	"time"
)

const (
	PatchWindowDays = 45

	// The cluster "zero-day" is the build date of the running kube-apiserver image.
	KubeAPIServerNamespace       = "kube-system"
	KubeAPIServerSelector        = "component=kube-apiserver"
	KubeAPIServerImageRepository = "rancher/hardened-kubernetes"
)

// Reasons a target tag is refused. They double as ImagePatch condition reasons.
const (
	ReasonTagNotFound        = "TagNotFound"
	ReasonUnparsableTag      = "UnparsableTag"
	ReasonNotNewer           = "NotNewer"
	ReasonMinorVersionChange = "MinorVersionChange"
	ReasonOutsidePatchWindow = "OutsidePatchWindow"
)

// Violation is a policy refusal with a machine-readable reason
type Violation struct {
	Reason  string
	Message string
}

func (v *Violation) Error() string { return v.Message }

func violation(reason string, format string, args ...any) *Violation {
	return &Violation{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// IsPatchWindowExempt reports whether a component can be patched regardless of the patch window
func IsPatchWindowExempt(componentName string) bool {
	return strings.EqualFold(strings.TrimSpace(componentName), "rke2-ingress-nginx")
}

// ValidatePatchWindow refuses target tags built more than PatchWindowDays after the cluster zero-day
func ValidatePatchWindow(componentName string, targetTag string, zeroDay time.Time) error {
	if IsPatchWindowExempt(componentName) {
		return nil
	}

	targetDate, ok := BuildDate(targetTag)
	if !ok {
		return violation(ReasonUnparsableTag, "refusing to patch: target tag %q does not contain a build date required for the %d-day patch window", targetTag, PatchWindowDays)
	}

	deadline := zeroDay.AddDate(0, 0, PatchWindowDays)
	if targetDate.After(deadline) {
		return violation(ReasonOutsidePatchWindow,
			"refusing to patch: target tag %q (build date %s) is outside the %d-day window from cluster zero-day %s; upgrade RKE2 to continue patching",
			targetTag,
			targetDate.Format("2006-01-02"),
			PatchWindowDays,
			zeroDay.Format("2006-01-02"),
		)
	}

	return nil
}

// ValidateExplicitTarget checks a requested target tag against the baseline (bundled) tag:
// it must be published, newer than the baseline and on the same major/minor release line.
func ValidateExplicitTarget(baselineTag string, targetTag string, availableTags []string) error {
	found := false
	for _, name := range availableTags {
		if name == targetTag {
			found = true
			break
		}
	}
	if !found {
		return violation(ReasonTagNotFound, "refusing to patch: requested target tag %q was not found in latest observed tags", targetTag)
	}

	return ValidateAgainstBaseline(baselineTag, targetTag)
}

// ValidateNotOlderThanCurrent refuses moving back to a tag older than the one currently
// applied, like the CLI does with the running tag. Rolling back means reverting to the
// bundled tag first.
func ValidateNotOlderThanCurrent(currentTag string, targetTag string) error {
	current, currentOK := ParseTag(currentTag)
	target, targetOK := ParseTag(targetTag)
	if !currentOK || !targetOK {
		return nil
	}
	if CompareTags(target, current) < 0 {
		return violation(ReasonNotNewer, "refusing to patch: requested target tag %q is older than current tag %q", targetTag, currentTag)
	}
	return nil
}

// ValidateAgainstBaseline checks that a target tag is newer than the baseline (bundled) tag
// and on the same major/minor release line
func ValidateAgainstBaseline(baselineTag string, targetTag string) error {
	baseline, ok := ParseTag(baselineTag)
	if !ok {
		return violation(ReasonUnparsableTag, "refusing to patch: current tag %q cannot be compared for minor compatibility", baselineTag)
	}
	target, ok := ParseTag(targetTag)
	if !ok {
		return violation(ReasonUnparsableTag, "refusing to patch: target tag %q cannot be compared for minor compatibility", targetTag)
	}

	if CompareTags(target, baseline) <= 0 {
		return violation(ReasonNotNewer, "refusing to patch: requested target tag %q is not newer than the bundled tag %q", targetTag, baselineTag)
	}
	if target.Major != baseline.Major || target.Minor != baseline.Minor {
		return violation(ReasonMinorVersionChange, "refusing to patch: moving to a newer minor release is not supported; the target tag must be on the same major/minor release line (current: %q, target: %q)", baselineTag, targetTag)
	}

	return nil
}
