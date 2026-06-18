package cmd

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/rancher/rke2-patcher/internal/registry"
	cli "github.com/urfave/cli/v2"
)

func TestRunImageListCommandVerboseRequiresWithCVEs(t *testing.T) {
	app := BuildCLIApp()
	set := flag.NewFlagSet("image-list", flag.ContinueOnError)
	set.Bool("with-cves", false, "")
	set.Bool("verbose", false, "")

	if err := set.Parse([]string{"--verbose", "rke2-traefik"}); err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}

	ctx := cli.NewContext(app, set, nil)
	err := runImageListCommand(ctx)
	if err == nil {
		t.Fatalf("expected validation error, got nil")
	}

	if !strings.Contains(err.Error(), "--verbose requires --with-cves") {
		t.Fatalf("unexpected error: %v", err)
	}

	var exitErr cli.ExitCoder
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected cli exit error, got %T", err)
	}
	if exitErr.ExitCode() != 2 {
		t.Fatalf("unexpected exit code: %d", exitErr.ExitCode())
	}
}

func TestRunImagePatchCommandRejectsExtraArguments(t *testing.T) {
	app := BuildCLIApp()
	set := flag.NewFlagSet("image-patch", flag.ContinueOnError)
	set.Bool("dry-run", false, "")

	if err := set.Parse([]string{"--dry-run", "rke2-traefik", "extra"}); err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}

	ctx := cli.NewContext(app, set, nil)
	err := runImagePatchCommand(ctx)
	if err == nil {
		t.Fatalf("expected validation error, got nil")
	}

	if !strings.Contains(err.Error(), "unexpected extra argument(s): extra") {
		t.Fatalf("unexpected error: %v", err)
	}

	var exitErr cli.ExitCoder
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected cli exit error, got %T", err)
	}
	if exitErr.ExitCode() != 2 {
		t.Fatalf("unexpected exit code: %d", exitErr.ExitCode())
	}
}

func TestParseComparableTag(t *testing.T) {
	t.Run("version build tag", func(t *testing.T) {
		tag, ok := parseComparableTag("v1.14.1-build20260206")
		if !ok {
			t.Fatalf("expected tag to be parseable")
		}

		if tag.Major != 1 || tag.Minor != 14 || tag.Patch != 1 || tag.Build != 20260206 {
			t.Fatalf("unexpected parsed tag: %#v", tag)
		}
	})

	t.Run("lts build tag", func(t *testing.T) {
		tag, ok := parseComparableTag("v1.12.0-lts1-build20250210")
		if !ok {
			t.Fatalf("expected lts tag to be parseable")
		}

		if tag.Flavor != "lts1" {
			t.Fatalf("unexpected flavor: %q", tag.Flavor)
		}
	})

	t.Run("hardened tag", func(t *testing.T) {
		tag, ok := parseComparableTag("v1.14.5-hardened1")
		if !ok {
			t.Fatalf("expected hardened tag to be parseable")
		}

		if tag.Major != 1 || tag.Minor != 14 || tag.Patch != 5 {
			t.Fatalf("unexpected version parts: %#v", tag)
		}
		if tag.Build != 0 {
			t.Fatalf("expected no build number, got %#v", tag)
		}
		if tag.Flavor != "hardened1" || tag.FlavorBase != "hardened" || tag.FlavorNumber != 1 {
			t.Fatalf("unexpected hardened flavor parse: %#v", tag)
		}
	})

	t.Run("prime tag", func(t *testing.T) {
		tag, ok := parseComparableTag("v1.14.5-prime3")
		if !ok {
			t.Fatalf("expected prime tag to be parseable")
		}

		if tag.Major != 1 || tag.Minor != 14 || tag.Patch != 5 {
			t.Fatalf("unexpected version parts: %#v", tag)
		}
		if tag.Build != 0 {
			t.Fatalf("expected no build number, got %#v", tag)
		}
		if tag.Flavor != "prime3" || tag.FlavorBase != "prime" || tag.FlavorNumber != 3 {
			t.Fatalf("unexpected prime flavor parse: %#v", tag)
		}
	})

	t.Run("plain semver tag", func(t *testing.T) {
		tag, ok := parseComparableTag("v1.40.7")
		if !ok {
			t.Fatalf("expected plain semver tag to be parseable")
		}

		if tag.Major != 1 || tag.Minor != 40 || tag.Patch != 7 || tag.Build != 0 {
			t.Fatalf("unexpected parsed tag: %#v", tag)
		}
	})

	t.Run("signature tag excluded", func(t *testing.T) {
		if _, ok := parseComparableTag("sha256-1234.sig"); ok {
			t.Fatalf("expected signature tag to be excluded")
		}
	})
}

func TestSelectTagsForCVEListing_OrderedAndFiltered(t *testing.T) {
	tags := []registry.Tag{
		{Name: "sha256-063f303c.att"},
		{Name: "sha256-063f303c.sig"},
		{Name: "v1.10.1-build20230406"},
		{Name: "v1.12.4-build20251015"},
		{Name: "v1.14.1-build20260203"},
		{Name: "v1.14.1-build20260206"},
		{Name: "v1.14.2-build20260309"},
	}

	ordered, previous := selectTagsForCVEListing(tags, "v1.14.1-build20260206")

	expectedOrdered := []string{
		"v1.14.2-build20260309",
		"v1.14.1-build20260206",
		"v1.14.1-build20260203",
	}

	if !reflect.DeepEqual(ordered, expectedOrdered) {
		t.Fatalf("unexpected ordered tags: %#v", ordered)
	}

	if previous != "v1.14.1-build20260203" {
		t.Fatalf("unexpected previous tag: %q", previous)
	}
}

func TestOrderedComparableTags(t *testing.T) {
	tags := []registry.Tag{
		{Name: "v1.14.1-build20260206"},
		{Name: "v1.14.2-build20260309"},
		{Name: "v1.14.1-build20260203"},
		{Name: "sha256-1234.att"},
	}

	ordered := orderedComparableTags(tags)
	expected := []string{
		"v1.14.2-build20260309",
		"v1.14.1-build20260206",
		"v1.14.1-build20260203",
	}

	if !reflect.DeepEqual(ordered, expected) {
		t.Fatalf("unexpected ordered tags: %#v", ordered)
	}
}

func TestOrderedComparableTags_HardenedSuffixes(t *testing.T) {
	tags := []registry.Tag{
		{Name: "v1.14.5-hardened1"},
		{Name: "v1.14.5-hardened10"},
		{Name: "v1.14.5-hardened2"},
		{Name: "v1.14.4-hardened3"},
	}

	ordered := orderedComparableTags(tags)
	expected := []string{
		"v1.14.5-hardened10",
		"v1.14.5-hardened2",
		"v1.14.5-hardened1",
		"v1.14.4-hardened3",
	}

	if !reflect.DeepEqual(ordered, expected) {
		t.Fatalf("unexpected ordered tags: %#v", ordered)
	}
}

func TestOrderedComparableTags_PrimeSuffixes(t *testing.T) {
	tags := []registry.Tag{
		{Name: "v1.14.5-prime1"},
		{Name: "v1.14.5-prime10"},
		{Name: "v1.14.5-prime3"},
		{Name: "v1.14.4-prime9"},
	}

	ordered := orderedComparableTags(tags)
	expected := []string{
		"v1.14.5-prime10",
		"v1.14.5-prime3",
		"v1.14.5-prime1",
		"v1.14.4-prime9",
	}

	if !reflect.DeepEqual(ordered, expected) {
		t.Fatalf("unexpected ordered tags: %#v", ordered)
	}
}

func TestSelectTagsForCVEListing_HardenedTags(t *testing.T) {
	tags := []registry.Tag{
		{Name: "v1.14.4-hardened2"},
		{Name: "v1.14.5-hardened1"},
		{Name: "v1.14.5-hardened2"},
	}

	ordered, previous := selectTagsForCVEListing(tags, "v1.14.5-hardened1")
	expectedOrdered := []string{
		"v1.14.5-hardened2",
		"v1.14.5-hardened1",
		"v1.14.4-hardened2",
	}

	if !reflect.DeepEqual(ordered, expectedOrdered) {
		t.Fatalf("unexpected ordered tags: %#v", ordered)
	}

	if previous != "v1.14.4-hardened2" {
		t.Fatalf("unexpected previous tag: %q", previous)
	}
}

func TestResolvePatchTargetTag_AllowsHardenedUpgrade(t *testing.T) {
	repository := "rancher/nginx-ingress-controller"
	server := newTagsServer(t, repository, []string{
		"v1.14.5-hardened1",
		"v1.14.5-hardened2",
	})
	t.Setenv("RKE2_PATCHER_REGISTRY", server.URL)

	targetTag, err := resolvePatchTargetTag(repository, "v1.14.5-hardened1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if targetTag != "v1.14.5-hardened2" {
		t.Fatalf("unexpected target tag: %q", targetTag)
	}
}

func TestResolvePatchTargetTag_AllowsPrimeUpgrade(t *testing.T) {
	repository := "rancher/nginx-ingress-controller"
	server := newTagsServer(t, repository, []string{
		"v1.14.5-prime3",
		"v1.14.5-prime4",
	})
	t.Setenv("RKE2_PATCHER_REGISTRY", server.URL)

	targetTag, err := resolvePatchTargetTag(repository, "v1.14.5-prime3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if targetTag != "v1.14.5-prime4" {
		t.Fatalf("unexpected target tag: %q", targetTag)
	}
}

func TestResolvePatchTargetTag_RejectsNewerMinorUpgrade(t *testing.T) {
	repository := "rancher/hardened-traefik"
	server := newTagsServer(t, repository, []string{
		"v1.14.1-build20260206",
		"v1.15.0-build20260301",
	})
	t.Setenv("RKE2_PATCHER_REGISTRY", server.URL)

	_, err := resolvePatchTargetTag(repository, "v1.14.1-build20260206")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "moving to a newer minor release is not supported") {
		t.Fatalf("expected minor-upgrade guard error, got %q", err.Error())
	}
}

func TestResolvePatchTargetTag_AllowsSameMinorUpgrade(t *testing.T) {
	repository := "rancher/hardened-traefik"
	server := newTagsServer(t, repository, []string{
		"v1.14.1-build20260206",
		"v1.14.2-build20260309",
	})
	t.Setenv("RKE2_PATCHER_REGISTRY", server.URL)

	targetTag, err := resolvePatchTargetTag(repository, "v1.14.1-build20260206")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if targetTag != "v1.14.2-build20260309" {
		t.Fatalf("unexpected target tag: %q", targetTag)
	}
}

func newTagsServer(t *testing.T, repository string, tags []string) *httptest.Server {
	t.Helper()

	path := fmt.Sprintf("/v2/%s/tags/list", repository)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"tags":["%s"]}`, strings.Join(tags, `","`))
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return server
}

func useInMemoryPatchStateBackend(t *testing.T) {
	t.Helper()

	stored := patchState{Entries: map[string]patchEntry{}}
	originalLoad := loadPatchStateFromBackend
	originalSave := savePatchStateToBackend
	originalEnsureNamespace := ensureStateNamespace

	ensureStateNamespace = func(_ string) error {
		return nil
	}

	loadPatchStateFromBackend = func(_ string) (patchState, string, error) {
		copied := patchState{Entries: map[string]patchEntry{}}
		for key, entry := range stored.Entries {
			copied.Entries[key] = entry
		}
		return copied, "", nil
	}

	savePatchStateToBackend = func(_ string, state patchState, _ string) error {
		copied := patchState{Entries: map[string]patchEntry{}}
		for key, entry := range state.Entries {
			copied.Entries[key] = entry
		}
		stored = copied
		return nil
	}

	t.Cleanup(func() {
		loadPatchStateFromBackend = originalLoad
		savePatchStateToBackend = originalSave
		ensureStateNamespace = originalEnsureNamespace
	})
}

func TestEvaluatePatchEligibility_AllowsMultipleForwardPatchesPerComponentAndClusterVersion(t *testing.T) {
	useInMemoryPatchStateBackend(t)

	originalClusterVersionResolver := clusterVersionResolver
	clusterVersionResolver = func() (string, error) {
		return "v1.35.2+rke2r1", nil
	}
	t.Cleanup(func() {
		clusterVersionResolver = originalClusterVersionResolver
	})

	decision, err := generateStateWrite("rke2-traefik", "v3.6.7-build20260301", "v3.6.8-build20260302", "")
	if err != nil {
		t.Fatalf("unexpected error during first patch evaluation: %v", err)
	}

	if err := persistPatchDecision(decision); err != nil {
		t.Fatalf("unexpected persistence error: %v", err)
	}

	secondDecision, err := generateStateWrite("rke2-traefik", "v3.6.8-build20260302", "v3.6.9-build20260303", "")
	if err != nil {
		t.Fatalf("expected second forward patch to be allowed, got %v", err)
	}

	if err := persistPatchDecision(secondDecision); err != nil {
		t.Fatalf("unexpected second persistence error: %v", err)
	}

	state, _, err := loadPatchStateFromBackend(patchStateNamespace())
	if err != nil {
		t.Fatalf("unexpected state load error: %v", err)
	}

	entry, found := state.Entries["v1.35.2+rke2r1|rke2-traefik"]
	if !found {
		t.Fatalf("expected state entry for component")
	}

	if entry.BaselineTag != "v3.6.7-build20260301" {
		t.Fatalf("expected baseline tag to stay on first observed value, got %q", entry.BaselineTag)
	}

	if entry.PatchedToTag != "v3.6.9-build20260303" {
		t.Fatalf("expected patched-to tag to be updated, got %q", entry.PatchedToTag)
	}
}

func TestEvaluatePatchEligibility_RequiresReconcileAfterRKE2Upgrade(t *testing.T) {
	useInMemoryPatchStateBackend(t)

	clusterVersion := "v1.35.2+rke2r1"
	originalClusterVersionResolver := clusterVersionResolver
	clusterVersionResolver = func() (string, error) {
		return clusterVersion, nil
	}
	t.Cleanup(func() {
		clusterVersionResolver = originalClusterVersionResolver
	})

	firstDecision, err := generateStateWrite("rke2-traefik", "v3.6.7-build20260301", "v3.6.8-build20260302", "")
	if err != nil {
		t.Fatalf("unexpected first patch evaluation error: %v", err)
	}
	if err := persistPatchDecision(firstDecision); err != nil {
		t.Fatalf("unexpected first persistence error: %v", err)
	}

	clusterVersion = "v1.36.0+rke2r1"
	_, err = generateStateWrite("rke2-traefik", "v3.6.8-build20260302", "v3.6.9-build20260303", "")
	if err == nil {
		t.Fatalf("expected patch to be blocked until reconcile after cluster version change")
	}

	if !strings.Contains(err.Error(), "reconcile") {
		t.Fatalf("expected reconcile guidance after cluster version change, got %v", err)
	}
}
