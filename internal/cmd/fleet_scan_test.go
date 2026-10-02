package cmd

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/cve"
	"github.com/rancher/rke2-patcher/internal/kube"
	"github.com/rancher/rke2-patcher/internal/registry"
	cli "github.com/urfave/cli/v2"
)

// buildTag returns a parseable version tag with a build date offset by daysAgo from today.
func buildTag(version string, daysAgo int) string {
	return fmt.Sprintf("v%s-build%s", version, time.Now().AddDate(0, 0, -daysAgo).Format("20060102"))
}

type fleetScanConfigMapWrite struct {
	Namespace     string
	ConfigMapName string
	Content       string
}

// mockFleetScanDependencies wires every indirection point used by runFleetScan to in-memory fakes and
// restores the originals on test cleanup.
func mockFleetScanDependencies(t *testing.T, currentTag string, newerTag string, failingRepositories map[string]bool) *[]fleetScanConfigMapWrite {
	t.Helper()
	t.Setenv(patchStateNamespaceEnv, "")

	originalClusterZeroDayResolver := clusterZeroDayResolver
	clusterZeroDayResolver = func() (time.Time, error) {
		return time.Now().AddDate(0, 0, -30), nil
	}

	originalListRunningImages := listRunningImagesForFleetScan
	listRunningImagesForFleetScan = func(workload components.WorkloadRef, repository string) ([]kube.PodImageSummary, error) {
		if failingRepositories[repository] {
			return nil, errors.New("simulated running image lookup failure")
		}
		return []kube.PodImageSummary{{Image: fmt.Sprintf("%s:%s", repository, currentTag), Count: 1}}, nil
	}

	originalListTags := listTagsForFleetScan
	listTagsForFleetScan = func(repository string, limit int) ([]registry.Tag, error) {
		return []registry.Tag{{Name: currentTag}, {Name: newerTag}}, nil
	}

	originalListCVEs := listCVEsForImagesForFleetScan
	listCVEsForImagesForFleetScan = func(images []string) (map[string]cve.ResultCVEs, map[string]error, error) {
		results := make(map[string]cve.ResultCVEs, len(images))
		for _, image := range images {
			results[image] = cve.ResultCVEs{Tool: "trivy-job-batch", CVEs: []cve.Vulnerability{
				{ID: "CVE-2024-0001", Severity: "HIGH"},
				{ID: "CVE-2024-0002", Severity: "LOW"},
			}}
		}
		return results, map[string]error{}, nil
	}

	var recordedWrites []fleetScanConfigMapWrite
	originalWrite := writeFleetReportConfigMap
	writeFleetReportConfigMap = func(namespace string, configMapName string, content string) error {
		recordedWrites = append(recordedWrites, fleetScanConfigMapWrite{Namespace: namespace, ConfigMapName: configMapName, Content: content})
		return nil
	}

	originalEnsureNamespace := ensureStateNamespace
	ensureStateNamespace = func(_ string) error { return nil }

	fixedNow := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	originalNow := fleetScanNow
	fleetScanNow = func() time.Time { return fixedNow }

	t.Cleanup(func() {
		clusterZeroDayResolver = originalClusterZeroDayResolver
		listRunningImagesForFleetScan = originalListRunningImages
		listTagsForFleetScan = originalListTags
		listCVEsForImagesForFleetScan = originalListCVEs
		writeFleetReportConfigMap = originalWrite
		ensureStateNamespace = originalEnsureNamespace
		fleetScanNow = originalNow
	})

	return &recordedWrites
}

// currentTagEntry returns the "current" status tag entry out of a component report, failing the test if absent.
func currentTagEntry(t *testing.T, componentReport fleetComponentReport) imageListTagJSON {
	t.Helper()
	for _, tag := range componentReport.Tags {
		if tag.Status == "current" {
			return tag
		}
	}
	t.Fatalf("expected a %q-status tag for component %q", "current", componentReport.Component)
	return imageListTagJSON{}
}

func TestRunFleetScan_AggregatesAllComponentsAndWritesConfigMap(t *testing.T) {
	currentTag := buildTag("1.0.0", 10)
	newerTag := buildTag("1.0.1", 2)
	writes := mockFleetScanDependencies(t, currentTag, newerTag, nil)

	if err := runFleetScan(fleetScanOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*writes) != 1 {
		t.Fatalf("expected exactly one ConfigMap write, got %d", len(*writes))
	}

	write := (*writes)[0]
	if write.Namespace != defaultPatchStateNamespace {
		t.Fatalf("unexpected namespace: %q", write.Namespace)
	}
	if write.ConfigMapName != defaultFleetScanConfigMapName {
		t.Fatalf("expected default ConfigMap name, got %q", write.ConfigMapName)
	}

	var report fleetScanReport
	if err := json.Unmarshal([]byte(write.Content), &report); err != nil {
		t.Fatalf("failed to unmarshal written report: %v", err)
	}

	expectedComponents := components.Supported()
	if len(report.Components) != len(expectedComponents) {
		t.Fatalf("expected %d component reports, got %d", len(expectedComponents), len(report.Components))
	}

	for _, componentReport := range report.Components {
		if componentReport.Error != "" {
			t.Fatalf("unexpected error for component %q: %s", componentReport.Component, componentReport.Error)
		}
		if len(componentReport.Tags) != 2 {
			t.Fatalf("expected 2 scanned tags for %q, got %d", componentReport.Component, len(componentReport.Tags))
		}

		current := currentTagEntry(t, componentReport)
		if current.Tag != currentTag {
			t.Fatalf("unexpected current tag for %q: %q", componentReport.Component, current.Tag)
		}

		for _, tagReport := range componentReport.Tags {
			if tagReport.CVEs == nil || tagReport.CVEs.Count == nil {
				t.Fatalf("expected CVE data for tag %q of %q", tagReport.Tag, componentReport.Component)
			}
			if *tagReport.CVEs.Count != 2 {
				t.Fatalf("expected 2 CVEs for tag %q of %q, got %d", tagReport.Tag, componentReport.Component, *tagReport.CVEs.Count)
			}
			if tagReport.CVEs.Vulnerabilities == nil || len(*tagReport.CVEs.Vulnerabilities) != 2 {
				t.Fatalf("expected 2 vulnerability entries for tag %q of %q", tagReport.Tag, componentReport.Component)
			}
			if (*tagReport.CVEs.Vulnerabilities)[0].Severity != "HIGH" {
				t.Fatalf("expected severity to be carried through, got %q", (*tagReport.CVEs.Vulnerabilities)[0].Severity)
			}
		}
	}
}

func TestRunFleetScan_ContinuesAfterSingleComponentFailure(t *testing.T) {
	currentTag := buildTag("1.0.0", 10)
	newerTag := buildTag("1.0.1", 2)
	failing := map[string]bool{"rancher/hardened-traefik": true}
	writes := mockFleetScanDependencies(t, currentTag, newerTag, failing)

	if err := runFleetScan(fleetScanOptions{}); err != nil {
		t.Fatalf("expected overall success when only one component fails, got: %v", err)
	}

	var report fleetScanReport
	if err := json.Unmarshal([]byte((*writes)[0].Content), &report); err != nil {
		t.Fatalf("failed to unmarshal written report: %v", err)
	}

	foundFailure := false
	for _, componentReport := range report.Components {
		if componentReport.Component == "rke2-traefik" {
			foundFailure = true
			if componentReport.Error == "" {
				t.Fatalf("expected rke2-traefik to report an error")
			}
			if !strings.Contains(componentReport.Error, "simulated running image lookup failure") {
				t.Fatalf("unexpected error message: %q", componentReport.Error)
			}
			continue
		}
		if componentReport.Error != "" {
			t.Fatalf("unexpected error for component %q: %s", componentReport.Component, componentReport.Error)
		}
	}

	if !foundFailure {
		t.Fatalf("expected a report entry for rke2-traefik")
	}
}

func TestRunFleetScan_ReturnsErrorWhenEveryComponentFails(t *testing.T) {
	currentTag := buildTag("1.0.0", 10)
	newerTag := buildTag("1.0.1", 2)

	failing := map[string]bool{}
	for _, name := range components.Supported() {
		component, err := components.Resolve(name)
		if err != nil {
			t.Fatalf("failed to resolve %q: %v", name, err)
		}
		failing[component.Repository] = true
	}

	writes := mockFleetScanDependencies(t, currentTag, newerTag, failing)

	err := runFleetScan(fleetScanOptions{})
	if err == nil {
		t.Fatalf("expected error when every component fails")
	}
	if !strings.Contains(err.Error(), "failed for all") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// The report is still written to the ConfigMap even though every component failed.
	if len(*writes) != 1 {
		t.Fatalf("expected the report to still be written, got %d writes", len(*writes))
	}
}

func TestRunFleetScan_UsesCustomConfigMapNameAndJSONOutput(t *testing.T) {
	currentTag := buildTag("1.0.0", 10)
	newerTag := buildTag("1.0.1", 2)
	writes := mockFleetScanDependencies(t, currentTag, newerTag, nil)

	if err := runFleetScan(fleetScanOptions{ConfigMapName: "custom-report", JSON: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if (*writes)[0].ConfigMapName != "custom-report" {
		t.Fatalf("expected custom ConfigMap name to be used, got %q", (*writes)[0].ConfigMapName)
	}
}

func TestRunFleetScanCommand_RejectsExtraArguments(t *testing.T) {
	app := BuildCLIApp()
	set := flag.NewFlagSet("fleet-scan", flag.ContinueOnError)

	if err := set.Parse([]string{"unexpected-arg"}); err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}

	ctx := cli.NewContext(app, set, nil)
	err := runFleetScanCommand(ctx)
	if err == nil {
		t.Fatalf("expected validation error, got nil")
	}

	if !strings.Contains(err.Error(), "unexpected extra argument(s): unexpected-arg") {
		t.Fatalf("unexpected error: %v", err)
	}
}
