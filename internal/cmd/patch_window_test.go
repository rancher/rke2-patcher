package cmd

import (
	"strings"
	"testing"
	"time"
)

func TestBuildDateFromTag(t *testing.T) {
	date, ok := buildDateFromTag("v1.35.4-rke2r1-build20260416")
	if !ok {
		t.Fatalf("expected build date to be parsed")
	}

	if date.Format("2006-01-02") != "2026-04-16" {
		t.Fatalf("unexpected build date: %s", date.Format("2006-01-02"))
	}

	if _, ok := buildDateFromTag("v1.14.5-prime3"); ok {
		t.Fatalf("expected tag without build marker to be rejected")
	}
}

func TestValidatePatchWindow_AllowsTargetInsideWindow(t *testing.T) {
	originalResolver := clusterZeroDayResolver
	clusterZeroDayResolver = func() (time.Time, error) {
		return time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC), nil
	}
	t.Cleanup(func() { clusterZeroDayResolver = originalResolver })

	err := validatePatchWindow("rke2-traefik", "v3.6.12-build20260520")
	if err != nil {
		t.Fatalf("expected tag inside window to be allowed, got %v", err)
	}
}

func TestValidatePatchWindow_BlocksTargetOutsideWindow(t *testing.T) {
	originalResolver := clusterZeroDayResolver
	clusterZeroDayResolver = func() (time.Time, error) {
		return time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC), nil
	}
	t.Cleanup(func() { clusterZeroDayResolver = originalResolver })

	err := validatePatchWindow("rke2-traefik", "v3.6.12-build20260630")
	if err == nil {
		t.Fatalf("expected tag outside window to be blocked")
	}

	if !strings.Contains(err.Error(), "outside the 45-day window") {
		t.Fatalf("expected window error, got %v", err)
	}

	if !strings.Contains(err.Error(), "upgrade RKE2") {
		t.Fatalf("expected upgrade guidance, got %v", err)
	}
}

func TestValidatePatchWindow_ExemptsIngressNginx(t *testing.T) {
	originalResolver := clusterZeroDayResolver
	clusterZeroDayResolver = func() (time.Time, error) {
		return time.Time{}, nil
	}
	t.Cleanup(func() { clusterZeroDayResolver = originalResolver })

	err := validatePatchWindow("rke2-ingress-nginx", "v1.14.5-prime999")
	if err != nil {
		t.Fatalf("expected ingress-nginx to bypass patch window, got %v", err)
	}
}

func TestSplitTagsByPatchWindow_PreservesCurrentAndPreviousAndBlocksNewerOutsideWindow(t *testing.T) {
	originalResolver := clusterZeroDayResolver
	clusterZeroDayResolver = func() (time.Time, error) {
		return time.Date(2026, 2, 6, 0, 0, 0, 0, time.UTC), nil
	}
	t.Cleanup(func() { clusterZeroDayResolver = originalResolver })

	tags := []string{
		"v1.14.3-build20260423",
		"v1.14.2-build20260310",
		"v1.14.2-build20260309",
		"v1.14.1-build20260206",
		"v1.14.1-build20260203",
	}

	eligible, blocked, err := splitTagsByPatchWindow("rke2-coredns", tags, "v1.14.1-build20260206", "v1.14.1-build20260203")
	if err != nil {
		t.Fatalf("unexpected split error: %v", err)
	}

	expectedEligible := []string{
		"v1.14.2-build20260310",
		"v1.14.2-build20260309",
		"v1.14.1-build20260206",
		"v1.14.1-build20260203",
	}
	if strings.Join(eligible, ",") != strings.Join(expectedEligible, ",") {
		t.Fatalf("unexpected eligible tags: %#v", eligible)
	}

	expectedBlocked := []string{"v1.14.3-build20260423"}
	if strings.Join(blocked, ",") != strings.Join(expectedBlocked, ",") {
		t.Fatalf("unexpected blocked tags: %#v", blocked)
	}
}

func TestSplitTagsByPatchWindow_ExemptsIngressNginx(t *testing.T) {
	tags := []string{"v1.14.5-prime10", "v1.14.5-prime3", "v1.14.4-prime9"}

	eligible, blocked, err := splitTagsByPatchWindow("rke2-ingress-nginx", tags, "v1.14.5-prime3", "v1.14.4-prime9")
	if err != nil {
		t.Fatalf("unexpected split error: %v", err)
	}

	if len(blocked) != 0 {
		t.Fatalf("expected no blocked tags for ingress-nginx, got %#v", blocked)
	}

	if strings.Join(eligible, ",") != strings.Join(tags, ",") {
		t.Fatalf("expected all tags to remain eligible, got %#v", eligible)
	}
}
