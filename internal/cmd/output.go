package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/cve"
	"github.com/rancher/rke2-patcher/internal/kube"
)

type imageListJSON struct {
	Component     string             `json:"component"`
	Repository    string             `json:"repository"`
	RunningImages []runningImageJSON `json:"runningImages"`
	Tags          []imageListTagJSON `json:"tags"`
}

type runningImageJSON struct {
	Image string `json:"image"`
	Pods  int    `json:"pods"`
}

type imageListTagJSON struct {
	Tag           string             `json:"tag"`
	Status        string             `json:"status"`
	PatchEligible bool               `json:"patchEligible"`
	InUse         bool               `json:"inUse"`
	CVEs          *imageListCVEsJSON `json:"cves,omitempty"`
}

type imageListCVEsJSON struct {
	Count           *int                          `json:"count,omitempty"`
	Vulnerabilities *[]imageListVulnerabilityJSON `json:"vulnerabilities,omitempty"`
	Error           string                        `json:"error,omitempty"`
}

type imageListVulnerabilityJSON struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
}

type imageCVEJSON struct {
	Component string        `json:"component"`
	Image     string        `json:"image"`
	Scanner   string        `json:"scanner"`
	CVEs      imageCVEsJSON `json:"cves"`
}

type imageCVEsJSON struct {
	Count           int                          `json:"count"`
	Vulnerabilities []imageListVulnerabilityJSON `json:"vulnerabilities"`
}

func buildImageListJSON(component components.Component, runningImages []kube.PodImageSummary, tags []string, eligibleTags []string, currentTag string, previousTag string, cveByTag map[string]cveListEntry, includeCVEs bool) imageListJSON {
	output := imageListJSON{
		Component:     components.CLIName(component.Name),
		Repository:    component.Repository,
		RunningImages: make([]runningImageJSON, 0, len(runningImages)),
		Tags:          make([]imageListTagJSON, 0, len(tags)),
	}

	for _, image := range runningImages {
		output.RunningImages = append(output.RunningImages, runningImageJSON{Image: image.Image, Pods: image.Count})
	}

	eligibleSet := make(map[string]struct{}, len(eligibleTags))
	for _, tag := range eligibleTags {
		eligibleSet[tag] = struct{}{}
	}
	inUseTags := make(map[string]struct{})
	for _, image := range runningImages {
		_, tag := kube.SplitImage(image.Image)
		if tag != "" {
			inUseTags[tag] = struct{}{}
		}
	}

	for _, tagName := range tags {
		tag := imageListTagJSON{Tag: tagName, Status: "newer"}
		if tagName == currentTag {
			tag.Status = "current"
		} else if tagName == previousTag {
			tag.Status = "previous"
		}
		_, tag.PatchEligible = eligibleSet[tagName]
		_, tag.InUse = inUseTags[tagName]

		if includeCVEs {
			if entry, found := cveByTag[tagName]; found {
				tag.CVEs = &imageListCVEsJSON{}
				if strings.TrimSpace(entry.Error) != "" {
					tag.CVEs.Error = strings.TrimSpace(entry.Error)
				} else {
					count := len(entry.CVEs)
					vulnerabilities := make([]imageListVulnerabilityJSON, 0, len(entry.CVEs))
					for _, vulnerability := range entry.CVEs {
						vulnerabilities = append(vulnerabilities, imageListVulnerabilityJSON{
							ID:       vulnerability.ID,
							Severity: vulnerability.Severity,
						})
					}
					tag.CVEs.Count = &count
					tag.CVEs.Vulnerabilities = &vulnerabilities
				}
			}
		}

		output.Tags = append(output.Tags, tag)
	}

	return output
}

func writeImageListJSON(output imageListJSON) error {
	return encodeJSON(os.Stdout, output)
}

func encodeImageListJSON(writer io.Writer, output imageListJSON) error {
	return encodeJSON(writer, output)
}

func writeImageCVEJSON(output imageCVEJSON) error {
	return encodeJSON(os.Stdout, output)
}

//encodeJSON converts Go values to JSON and writes them to the provided writer
func encodeJSON(writer io.Writer, output any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func buildImageCVEJSON(component components.Component, image string, result cve.ResultCVEs) imageCVEJSON {
	vulnerabilities := make([]imageListVulnerabilityJSON, 0, len(result.CVEs))
	for _, vulnerability := range result.CVEs {
		vulnerabilities = append(vulnerabilities, imageListVulnerabilityJSON{
			ID:       vulnerability.ID,
			Severity: vulnerability.Severity,
		})
	}

	return imageCVEJSON{
		Component: components.CLIName(component.Name),
		Image:     image,
		Scanner:   result.Tool,
		CVEs: imageCVEsJSON{
			Count:           len(vulnerabilities),
			Vulnerabilities: vulnerabilities,
		},
	}
}

func printImageListWithCVEs(component components.Component, tagsToScan []string, currentTag string, previousTag string, cveByTag map[string]cveListEntry, verbose bool) {
	fmt.Printf("COMPONENT:  %s\n", components.CLIName(component.Name))
	fmt.Printf("REPOSITORY: %s\n\n", component.Repository)
	fmt.Printf("%-24s %-10s %-10s %s\n", "TAG", "STATUS", "CVE COUNT", "VULNERABILITIES")

	for _, tagName := range tagsToScan {
		status := "NEWER"
		switch tagName {
		case currentTag:
			status = "CURRENT*"
		case previousTag:
			status = "PREVIOUS"
		}

		count, vulnerabilities := renderCVESummary(cveByTag[tagName], verbose)
		fmt.Printf("%-24s %-10s %-10s %s\n", tagName, status, count, vulnerabilities)
	}
}

func printUpgradeRequiredTagsNotice(blockedTags []string) {
	if len(blockedTags) == 0 {
		return
	}

	fmt.Printf("\n%d additional newer tag(s) are available but require an RKE2 upgrade, so they were not scanned:\n", len(blockedTags))
	for _, tagName := range blockedTags {
		fmt.Printf("- %s\n", tagName)
	}
	fmt.Printf("Upgrade RKE2 to make these tags eligible for patching.\n")
}

func printPatchPreview(componentName, runningImage, currentTag, targetTag, content string) {
	fmt.Printf("component: %s\n", componentName)
	fmt.Printf("current image: %s\n", runningImage)
	fmt.Printf("current tag: %s\n", currentTag)
	fmt.Printf("new tag: %s\n", targetTag)
	fmt.Printf("dry-run: true\n")
	fmt.Printf("would apply HelmChartConfig\n")
	fmt.Println("---")
	fmt.Print(content)
}

func printPatchApplied(componentName, runningImage, currentTag, targetTag string) {
	fmt.Printf("component: %s\n", componentName)
	fmt.Printf("current image: %s\n", runningImage)
	fmt.Printf("current tag: %s\n", currentTag)
	fmt.Printf("new tag: %s\n", targetTag)
	fmt.Printf("applied HelmChartConfig\n")
}

func renderCVESummary(entry cveListEntry, verbose bool) (string, string) {
	if strings.TrimSpace(entry.Error) != "" {
		message := strings.TrimSpace(entry.Error)
		if !verbose {
			message = truncateText(message, 80)
		}
		return "ERR", "scan error: " + message
	}

	if len(entry.CVEs) == 0 {
		return "0", "none"
	}

	count := fmt.Sprintf("%d", len(entry.CVEs))
	visibleCVEs := entry.CVEs
	if !verbose && len(visibleCVEs) > 2 {
		visibleCVEs = visibleCVEs[:2]
	}

	formattedCVEs := make([]string, 0, len(visibleCVEs))
	for _, vulnerability := range visibleCVEs {
		formattedCVEs = append(formattedCVEs, fmt.Sprintf("%s (%s)", vulnerability.ID, vulnerability.Severity))
	}

	vulnerabilities := strings.Join(formattedCVEs, ", ")
	if !verbose && len(entry.CVEs) > len(visibleCVEs) {
		vulnerabilities += "..."
	}

	return count, vulnerabilities
}

func printReconcileApplied(entry patchEntry) {
	fmt.Printf("reconcile: component %s: stripped patcher overrides (was pinned to %s on RKE2 %s)\n", components.CLIName(entry.Component), entry.PatchedToTag, entry.ClusterVersion)
}

func printReconcileAlreadyCurrent(componentName string) {
	fmt.Printf("reconcile: component %s: no stale patches found; already up to date\n", componentName)
}

func truncateText(value string, maxLength int) string {
	if maxLength <= 0 {
		return ""
	}
	if len(value) <= maxLength {
		return value
	}
	if maxLength <= 3 {
		return value[:maxLength]
	}
	return value[:maxLength-3] + "..."
}
