package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/cve"
	"github.com/rancher/rke2-patcher/internal/kube"
	"github.com/rancher/rke2-patcher/internal/registry"
)

const defaultFleetScanConfigMapName = "rke2-patcher-report"

// Indirection points so tests can mock the underlying scan/list/Kubernetes calls per component.
var (
	listRunningImagesForFleetScan = kube.ListRunningImages
	listTagsForFleetScan          = registry.ListTags
	listCVEsForImagesForFleetScan = cve.ListCVEsForImages
	writeFleetReportConfigMap     = kube.SaveReportConfigMapData
	fleetScanNow                  = time.Now
)

type fleetScanOptions struct {
	ConfigMapName string
	JSON          bool
}

// fleetComponentReport is one component's entry in the combined fleet-scan report. On success it embeds
// the same "image-list --with-cves --json" shape built by buildImageListJSON (including per-tag CVE
// severity); on failure Error is set and the embedded fields are left at their zero values.
type fleetComponentReport struct {
	imageListJSON
	Error string `json:"error,omitempty"`
}

// fleetScanReport is the combined report written to the ConfigMap and printed to stdout.
type fleetScanReport struct {
	GeneratedAt time.Time              `json:"generatedAt"`
	Components  []fleetComponentReport `json:"components"`
}

// runFleetScan loops over every supported component, reusing the same image-list/CVE scan logic as
// "image-list --with-cves --json" for each one, aggregates the results into a single report, prints it,
// and writes it to the configured ConfigMap. A scan failure on one component is recorded in that
// component's report entry rather than aborting the whole run.
func runFleetScan(options fleetScanOptions) error {
	names := components.Supported()
	report := fleetScanReport{
		GeneratedAt: fleetScanNow().UTC(),
		Components:  make([]fleetComponentReport, 0, len(names)),
	}

	succeeded := 0
	for _, name := range names {
		componentReport := scanComponentForFleetReport(name)
		if componentReport.Error == "" {
			succeeded++
		}
		report.Components = append(report.Components, componentReport)
	}

	reportJSON, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize fleet scan report: %w", err)
	}

	if options.JSON {
		fmt.Println(string(reportJSON))
	} else {
		printFleetScanReport(report)
	}

	configMapName := strings.TrimSpace(options.ConfigMapName)
	if configMapName == "" {
		configMapName = defaultFleetScanConfigMapName
	}

	namespace := patchStateNamespace()
	if err := ensureStateNamespace(namespace); err != nil {
		return fmt.Errorf("failed to prepare namespace %q for fleet scan report: %w", namespace, err)
	}
	if err := writeFleetReportConfigMap(namespace, configMapName, string(reportJSON)); err != nil {
		return fmt.Errorf("failed to write fleet scan report to ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	if len(names) > 0 && succeeded == 0 {
		return fmt.Errorf("fleet-scan failed for all %d component(s); see report for details", len(names))
	}

	return nil
}

// scanComponentForFleetReport runs the same image-list/CVE scan logic used by "image-list --with-cves --json"
// for a single component (see runImageList/runImageListWithCVEs in run.go), returning its result rather than
// printing it directly.
func scanComponentForFleetReport(name string) fleetComponentReport {
	report := fleetComponentReport{}
	report.Component = components.CLIName(name)

	component, err := components.Resolve(name)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	report.Repository = component.Repository

	runningImages, err := listRunningImagesForFleetScan(component.Workload, component.Repository)
	if err != nil {
		report.Error = fmt.Sprintf("running image unavailable: %v", err)
		return report
	}

	// runningImages is ordered by descending pod count, so the first image is the most widely used one
	currentImage := runningImages[0].Image
	currentImageName, currentTag := kube.SplitImage(currentImage)
	if currentTag == "" {
		report.Error = fmt.Sprintf("running image %q does not include a tag", currentImage)
		return report
	}

	tagsForSelection, err := listTagsForFleetScan(component.Repository, 200)
	if err != nil {
		report.Error = fmt.Sprintf("failed to list tags: %v", err)
		return report
	}

	tagsToShow, previousTag := selectTagsForCVEListing(tagsForSelection, currentTag)
	if len(tagsToShow) == 0 {
		report.Error = fmt.Sprintf("failed to determine tags to scan for current tag %q", currentTag)
		return report
	}

	eligibleTags, _, err := splitTagsByPatchWindow(component.Name, tagsToShow, currentTag, previousTag)
	if err != nil {
		report.Error = fmt.Sprintf("failed to determine patch-window eligible tags: %v", err)
		return report
	}

	targetImages := make([]string, 0, len(eligibleTags))
	for _, tagName := range eligibleTags {
		targetImages = append(targetImages, fmt.Sprintf("%s:%s", currentImageName, tagName))
	}

	resultsByImage, errorsByImage, err := listCVEsForImagesForFleetScan(targetImages)
	if err != nil {
		report.Error = fmt.Sprintf("failed to scan images for CVEs: %v", err)
		return report
	}

	cveByTag := make(map[string]cveListEntry, len(eligibleTags))
	for _, tagName := range eligibleTags {
		targetImage := fmt.Sprintf("%s:%s", currentImageName, tagName)
		if imageErr, found := errorsByImage[targetImage]; found {
			cveByTag[tagName] = cveListEntry{Error: fmt.Sprintf("%v", imageErr)}
			continue
		}

		result, found := resultsByImage[targetImage]
		if !found {
			cveByTag[tagName] = cveListEntry{Error: "missing result"}
			continue
		}

		cveByTag[tagName] = cveListEntry{CVEs: result.CVEs}
	}

	report.imageListJSON = buildImageListJSON(component, runningImages, tagsToShow, eligibleTags, currentTag, previousTag, cveByTag, true)
	return report
}

// printFleetScanReport prints the combined report in the same tabular style used by "image-list --with-cves"
func printFleetScanReport(report fleetScanReport) {
	fmt.Printf("fleet-scan report generated: %s\n", report.GeneratedAt.Format(time.RFC3339))

	for _, componentReport := range report.Components {
		fmt.Println()
		fmt.Printf("COMPONENT:  %s\n", componentReport.Component)
		if componentReport.Error != "" {
			fmt.Printf("ERROR: %s\n", componentReport.Error)
			continue
		}

		fmt.Printf("REPOSITORY: %s\n\n", componentReport.Repository)
		fmt.Printf("%-24s %-10s %-10s %s\n", "TAG", "STATUS", "CVE COUNT", "VULNERABILITIES")
		for _, tag := range componentReport.Tags {
			count, vulnerabilities := renderFleetScanCVESummary(tag)
			status := strings.ToUpper(tag.Status)
			if !tag.PatchEligible {
				status += " (upgrade required)"
			}
			fmt.Printf("%-24s %-10s %-10s %s\n", tag.Tag, status, count, vulnerabilities)
		}
	}
}

// renderFleetScanCVESummary renders a tag's CVE count/vulnerability summary for the stdout table, mirroring
// renderCVESummary in output.go but operating on the already-built imageListTagJSON shape.
func renderFleetScanCVESummary(tag imageListTagJSON) (string, string) {
	if tag.CVEs == nil {
		return "-", "not scanned"
	}
	if strings.TrimSpace(tag.CVEs.Error) != "" {
		return "ERR", "scan error: " + strings.TrimSpace(tag.CVEs.Error)
	}
	if tag.CVEs.Count == nil || *tag.CVEs.Count == 0 {
		return "0", "none"
	}

	vulnerabilities := make([]string, 0, *tag.CVEs.Count)
	if tag.CVEs.Vulnerabilities != nil {
		for _, vulnerability := range *tag.CVEs.Vulnerabilities {
			vulnerabilities = append(vulnerabilities, fmt.Sprintf("%s (%s)", vulnerability.ID, vulnerability.Severity))
		}
	}

	return fmt.Sprintf("%d", *tag.CVEs.Count), strings.Join(vulnerabilities, ", ")
}
