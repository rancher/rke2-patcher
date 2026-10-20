package policy

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func reasonOf(err error) string {
	var violation *Violation
	if errors.As(err, &violation) {
		return violation.Reason
	}
	return ""
}

func TestValidateExplicitTarget(t *testing.T) {
	available := []string{"v3.4.0-build20260901", "v3.3.6-build20260912", "v3.3.4-build20260801", "v3.3.3-build20260701"}
	cases := []struct {
		target string
		reason string
	}{
		{target: "v3.3.6-build20260912", reason: ""},
		{target: "v3.3.9-build20260920", reason: ReasonTagNotFound},
		{target: "v3.3.3-build20260701", reason: ReasonNotNewer},
		{target: "v3.3.4-build20260801", reason: ReasonNotNewer},
		{target: "v3.4.0-build20260901", reason: ReasonMinorVersionChange},
	}
	for _, tc := range cases {
		err := ValidateExplicitTarget("v3.3.4-build20260801", tc.target, available)
		if got := reasonOf(err); got != tc.reason {
			t.Errorf("target %s: reason %q (err %v), want %q", tc.target, got, err, tc.reason)
		}
	}
}

func TestValidatePatchWindow(t *testing.T) {
	zeroDay := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)

	if err := ValidatePatchWindow("rke2-traefik", "v3.3.6-build20260929", zeroDay); err != nil {
		t.Fatalf("tag on the last day of the window refused: %v", err)
	}
	if reason := reasonOf(ValidatePatchWindow("rke2-traefik", "v3.3.6-build20260930", zeroDay)); reason != ReasonOutsidePatchWindow {
		t.Fatalf("tag after the window: reason %q", reason)
	}
	if err := ValidatePatchWindow("rke2-ingress-nginx", "v1.12.0-build20271231", zeroDay); err != nil {
		t.Fatalf("ingress-nginx must be exempt: %v", err)
	}
}

func TestOrderTagsNewestFirstAndFiltersSignatures(t *testing.T) {
	ordered := OrderTags([]string{"v1.0.0-build20260101", "sha256-abc.sig", "v1.0.1-build20260201", "latest", "v1.0.1-build20260201"})
	if len(ordered) != 2 || ordered[0] != "v1.0.1-build20260201" || ordered[1] != "v1.0.0-build20260101" {
		t.Fatalf("unexpected order: %v", ordered)
	}
}

// The controller reuses the CLI's wording so scenarios can assert the same message in both modes
func TestMinorVersionChangeUsesCLIWording(t *testing.T) {
	err := ValidateAgainstBaseline("v8.4.0-build20260205", "v8.5.0-build20260410")
	if err == nil || !strings.Contains(err.Error(), "refusing to patch: moving to a newer minor release is not supported") {
		t.Fatalf("unexpected message: %v", err)
	}
}
