package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/cve"
	"github.com/rancher/rke2-patcher/internal/kube"
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

func TestRenderCVESummaryIncludesSeverity(t *testing.T) {
	entry := cveListEntry{CVEs: []cve.Vulnerability{
		{ID: "CVE-1", Severity: "CRITICAL"},
		{ID: "CVE-2", Severity: "HIGH"},
		{ID: "CVE-3", Severity: "HIGH"},
	}}

	count, summary := renderCVESummary(entry, false)
	if count != "3" || summary != "CVE-1 (CRITICAL), CVE-2 (HIGH)..." {
		t.Fatalf("unexpected truncated summary: count=%q summary=%q", count, summary)
	}

	count, summary = renderCVESummary(entry, true)
	if count != "3" || summary != "CVE-1 (CRITICAL), CVE-2 (HIGH), CVE-3 (HIGH)" {
		t.Fatalf("unexpected verbose summary: count=%q summary=%q", count, summary)
	}
}

func TestImageListJSONIncludesTagMetadataAndCVEs(t *testing.T) {
	component := components.Component{Name: "rke2-traefik", Repository: "rancher/hardened-traefik"}
	runningImages := []kube.PodImageSummary{{Image: "registry.rancher.com/rancher/hardened-traefik:v-current", Count: 2}}
	tags := []string{"v-current", "v-previous", "v-blocked"}
	eligibleTags := []string{"v-current", "v-previous"}
	cveByTag := map[string]cveListEntry{
		"v-current":  {CVEs: []cve.Vulnerability{{ID: "CVE-1", Severity: "CRITICAL"}}},
		"v-previous": {CVEs: []cve.Vulnerability{}},
	}

	output := buildImageListJSON(component, runningImages, tags, eligibleTags, "v-current", "v-previous", cveByTag, true)
	var encoded bytes.Buffer
	if err := encodeImageListJSON(&encoded, output); err != nil {
		t.Fatalf("failed to encode image-list JSON: %v", err)
	}

	var decoded imageListJSON
	if err := json.Unmarshal(encoded.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to decode image-list JSON: %v", err)
	}
	if decoded.Component != "rke2-traefik" || decoded.Repository != "rancher/hardened-traefik" {
		t.Fatalf("unexpected component identity: %#v", decoded)
	}
	if len(decoded.RunningImages) != 1 || decoded.RunningImages[0].Pods != 2 {
		t.Fatalf("unexpected running images: %#v", decoded.RunningImages)
	}
	if len(decoded.Tags) != 3 {
		t.Fatalf("expected all selected tags, got %#v", decoded.Tags)
	}
	if decoded.Tags[0].Status != "current" || !decoded.Tags[0].PatchEligible || !decoded.Tags[0].InUse {
		t.Fatalf("unexpected current tag metadata: %#v", decoded.Tags[0])
	}
	if decoded.Tags[0].CVEs == nil || decoded.Tags[0].CVEs.Count == nil || *decoded.Tags[0].CVEs.Count != 1 || decoded.Tags[0].CVEs.Vulnerabilities == nil {
		t.Fatalf("unexpected current tag CVEs: %#v", decoded.Tags[0].CVEs)
	}
	if vulnerabilities := *decoded.Tags[0].CVEs.Vulnerabilities; len(vulnerabilities) != 1 || vulnerabilities[0].ID != "CVE-1" || vulnerabilities[0].Severity != "CRITICAL" {
		t.Fatalf("unexpected serialized vulnerability fields: %#v", vulnerabilities)
	}
	if decoded.Tags[1].CVEs == nil || decoded.Tags[1].CVEs.Count == nil || *decoded.Tags[1].CVEs.Count != 0 || decoded.Tags[1].CVEs.Vulnerabilities == nil {
		t.Fatalf("expected an empty CVE result for previous tag: %#v", decoded.Tags[1].CVEs)
	}
	if decoded.Tags[2].PatchEligible || decoded.Tags[2].CVEs != nil {
		t.Fatalf("blocked tag should not be marked eligible or scanned: %#v", decoded.Tags[2])
	}

	withoutCVEs := buildImageListJSON(component, runningImages, tags, eligibleTags, "v-current", "v-previous", nil, false)
	if withoutCVEs.Tags[0].CVEs != nil {
		t.Fatalf("unexpected CVE data when CVE scanning is disabled: %#v", withoutCVEs.Tags[0].CVEs)
	}
}

func TestImageCVEJSONIncludesMetadataAndEmptyArray(t *testing.T) {
	component := components.Component{Name: "rke2-traefik"}
	result := cve.ResultCVEs{
		Tool: "trivy-job",
		CVEs: []cve.Vulnerability{{ID: "CVE-1", Severity: "CRITICAL"}},
	}

	output := buildImageCVEJSON(component, "rancher/hardened-traefik:v1", result)
	var encoded bytes.Buffer
	if err := encodeJSON(&encoded, output); err != nil {
		t.Fatalf("failed to encode image-cve JSON: %v", err)
	}

	var decoded imageCVEJSON
	if err := json.Unmarshal(encoded.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to decode image-cve JSON: %v", err)
	}
	if decoded.Component != "rke2-traefik" || decoded.Image != "rancher/hardened-traefik:v1" || decoded.Scanner != "trivy-job" {
		t.Fatalf("unexpected image-cve metadata: %#v", decoded)
	}
	if decoded.CVEs.Count != 1 || len(decoded.CVEs.Vulnerabilities) != 1 || decoded.CVEs.Vulnerabilities[0].ID != "CVE-1" || decoded.CVEs.Vulnerabilities[0].Severity != "CRITICAL" {
		t.Fatalf("unexpected image-cve findings: %#v", decoded.CVEs)
	}

	empty := buildImageCVEJSON(component, "rancher/hardened-traefik:v1", cve.ResultCVEs{Tool: "trivy-job"})
	if empty.CVEs.Vulnerabilities == nil || len(empty.CVEs.Vulnerabilities) != 0 || empty.CVEs.Count != 0 {
		t.Fatalf("expected empty CVE list to serialize as an empty array: %#v", empty.CVEs)
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
