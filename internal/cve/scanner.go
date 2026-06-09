package cve

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/manuelbuil/rke2-patcher/internal/kube"
)

const cveModeEnv = "RKE2_PATCHER_CVE_MODE"

const (
	// Prefixes used to parse batch scan output from the cluster scanner job to frame each image's output
	batchScanBeginPrefix = "__RKE2_PATCHER_TRIVY_BEGIN__"
	batchScanRCPrefix    = "__RKE2_PATCHER_TRIVY_RC__"
	batchScanEndPrefix   = "__RKE2_PATCHER_TRIVY_END__"
	vexReportURL         = "https://github.com/rancher/vexhub/raw/refs/heads/main/reports/rancher.openvex.json"
	vexFileName          = "rancher.openvex.json"
	vexDownloadAttempts  = 5
	vexMaxFileAge        = 24 * time.Hour
)

type ResultCVEs struct {
	Tool string
	CVEs []string
}

// Indirection created to allow test mocking
var (
	scanImagesWithTrivyJob = kube.ScanImagesWithTrivyJob
	listCVEsForImageLocal  = scanImageLocally
)

// ListCVEsForImage scans the given image for CVEs using the appropriate scanning mode (local or cluster)
func ListCVEsForImage(image string) (ResultCVEs, error) {
	mode, err := resolveScanMode()
	if err != nil {
		return ResultCVEs{}, err
	}

	if mode == "local" {
		return listCVEsForImageLocal(image)
	}

	return listCVEsForImageInCluster(image)
}

// ListCVEsForImages scans a slice of images for CVEs using the appropriate scanning mode (local or cluster)
func ListCVEsForImages(images []string) (map[string]ResultCVEs, map[string]error, error) {
	mode, err := resolveScanMode()
	if err != nil {
		return nil, nil, err
	}

	if mode == "local" {
		results, errorsByImage := listCVEsForImagesLocal(images)
		return results, errorsByImage, nil
	}

	return listCVEsForImagesInCluster(images)
}

// listCVEsForImagesLocal scans each image individually with the local scanner
func listCVEsForImagesLocal(images []string) (map[string]ResultCVEs, map[string]error) {
	results := make(map[string]ResultCVEs)
	errorsByImage := make(map[string]error)
	for _, image := range images {
		result, scanErr := listCVEsForImageLocal(image)
		if scanErr != nil {
			errorsByImage[image] = scanErr
			continue
		}

		results[image] = result
	}

	return results, errorsByImage
}

// listCVEsForImageInCluster scans the given image with the cluster scanner and returns the CVEs found
func listCVEsForImageInCluster(image string) (ResultCVEs, error) {
	output, scanErr := kube.ScanImageWithTrivyJob(image)
	if scanErr == nil {
		cves, parseErr := trivyCVEsFromJSON(output)
		if parseErr == nil {
			return ResultCVEs{Tool: "trivy-job", CVEs: cves}, nil
		}
		scanErr = parseErr
	}
	return ResultCVEs{}, fmt.Errorf("cluster scanner failed: %v", scanErr)
}

// listCVEsForImagesInCluster scans the given images with the cluster scanner
func listCVEsForImagesInCluster(targetImages []string) (map[string]ResultCVEs, map[string]error, error) {
	output, err := scanImagesWithTrivyJob(targetImages, true)
	if err != nil {
		return nil, nil, fmt.Errorf("cluster scanner failed: %w", err)
	}

	results := make(map[string]ResultCVEs)
	errorsByImage := make(map[string]error)
	chunksByImage := make(map[string][]string)
	rcByImage := make(map[string]int)

	currentImage := ""
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		// The different prefixes are used to frame the output of each image's scan in the batch job output
		case strings.HasPrefix(line, batchScanBeginPrefix):
			currentImage = strings.TrimSpace(strings.TrimPrefix(line, batchScanBeginPrefix))
			if _, found := chunksByImage[currentImage]; !found {
				chunksByImage[currentImage] = make([]string, 0)
			}
		case strings.HasPrefix(line, batchScanRCPrefix):
			rest := strings.TrimSpace(strings.TrimPrefix(line, batchScanRCPrefix))
			separator := strings.LastIndex(rest, "__")
			if separator <= 0 {
				return nil, nil, fmt.Errorf("invalid batch scan marker: %q", line)
			}

			image := strings.TrimSpace(rest[:separator])
			rcText := strings.TrimSpace(rest[separator+2:])
			rc, parseErr := strconv.Atoi(rcText)
			if parseErr != nil {
				return nil, nil, fmt.Errorf("invalid batch scan rc marker %q: %w", line, parseErr)
			}
			rcByImage[image] = rc
		case strings.HasPrefix(line, batchScanEndPrefix):
			image := strings.TrimSpace(strings.TrimPrefix(line, batchScanEndPrefix))
			if currentImage == image {
				currentImage = ""
			}
		default:
			if currentImage != "" {
				chunksByImage[currentImage] = append(chunksByImage[currentImage], line)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}

	for _, image := range targetImages {
		rc, found := rcByImage[image]
		if !found {
			errorsByImage[image] = fmt.Errorf("missing scan result from batch job")
			continue
		}

		rawOutput := strings.TrimSpace(strings.Join(chunksByImage[image], "\n"))
		if rc != 0 {
			if rawOutput == "" {
				errorsByImage[image] = fmt.Errorf("trivy exited with code %d", rc)
			} else {
				errorsByImage[image] = fmt.Errorf("%s", rawOutput)
			}
			continue
		}

		cves, parseErr := trivyCVEsFromJSON([]byte(rawOutput))
		if parseErr != nil {
			errorsByImage[image] = fmt.Errorf("failed to parse trivy output: %w", parseErr)
			continue
		}

		results[image] = ResultCVEs{Tool: "trivy-job-batch", CVEs: cves}
	}

	return results, errorsByImage, nil
}

// scanImageLocally scans the given image with a local scanner and returns the CVEs found
func scanImageLocally(image string) (ResultCVEs, error) {
	errorsByMode := make([]string, 0)

	if _, lookErr := exec.LookPath("trivy"); lookErr == nil {
		cves, scanErr := trivyCVEs(image)
		if scanErr == nil {
			return ResultCVEs{Tool: "trivy", CVEs: cves}, nil
		}
		errorsByMode = append(errorsByMode, fmt.Sprintf("local trivy failed: %v", scanErr))
	}

	//grype is only used as fallback
	//TODO: Allow user to select grype too
	if _, lookErr := exec.LookPath("grype"); lookErr == nil {
		cves, scanErr := grypeCVEs(image)
		if scanErr == nil {
			return ResultCVEs{Tool: "grype", CVEs: cves}, nil
		}
		errorsByMode = append(errorsByMode, fmt.Sprintf("local grype failed: %v", scanErr))
	}

	if len(errorsByMode) == 0 {
		return ResultCVEs{}, fmt.Errorf("no local scanner available: install trivy or grype")
	}

	return ResultCVEs{}, fmt.Errorf("%s", strings.Join(errorsByMode, "; "))
}

// trivyCVEs scans the given image with local trivy and returns the list of CVEs found
func trivyCVEs(image string) ([]string, error) {
	vexFilePath, err := ensureLocalVEXFile()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("trivy", "image", "--quiet", "--format", "json", "--severity", "CRITICAL,HIGH", "--vex", vexFilePath, image)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	return trivyCVEsFromJSON(output)
}

// trivyCVEsFromJSON parses the JSON output of trivy and extracts the list of CVE IDs
func trivyCVEsFromJSON(output []byte) ([]string, error) {

	var report struct {
		Results []struct {
			Vulnerabilities []struct {
				VulnerabilityID string `json:"VulnerabilityID"`
			} `json:"Vulnerabilities"`
		} `json:"Results"`
	}

	if err := json.Unmarshal(output, &report); err != nil {
		return nil, err
	}

	return dedupeCVEs(func(appendCVE func(string)) {
		for _, result := range report.Results {
			for _, vulnerability := range result.Vulnerabilities {
				appendCVE(vulnerability.VulnerabilityID)
			}
		}
	}), nil
}

// ensureLocalVEXFile checks if a local VEX file is available and not very old (24h), and if not,
// attempts to download it from the hardcoded URL. It returns the path to the VEX file that can be used
// for local scans.
func ensureLocalVEXFile() (string, error) {
	homeDirectory, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve user home directory for local VEX configuration: %w", err)
	}

	vexDirectory := filepath.Join(homeDirectory, "rke2-patcher-cache", "vex")
	if mkdirErr := os.MkdirAll(vexDirectory, 0o755); mkdirErr != nil {
		return "", fmt.Errorf("failed to create trivy vex directory %q: %w", vexDirectory, mkdirErr)
	}

	vexFilePath := filepath.Join(vexDirectory, vexFileName)

	existingInfo, statErr := os.Stat(vexFilePath)
	hasExistingFile := false
	if statErr == nil {
		hasExistingFile = true
		age := time.Since(existingInfo.ModTime())
		if age <= vexMaxFileAge {
			log.Printf("using existing VEX file %q (age %s)", vexFilePath, age.Round(time.Second))
			return vexFilePath, nil
		}

		log.Printf("existing VEX file %q is older than %s (age %s); attempting to refresh", vexFilePath, vexMaxFileAge, age.Round(time.Second))
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("failed to check local VEX file %q: %w", vexFilePath, statErr)
	}

	var lastErr error
	for attempt := 1; attempt <= vexDownloadAttempts; attempt++ {
		err = downloadVEXFileOnce(vexDirectory, vexFilePath)
		if err == nil {
			log.Printf("downloaded VEX file to %q", vexFilePath)
			return vexFilePath, nil
		}

		lastErr = err
		if attempt < vexDownloadAttempts {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}

	if hasExistingFile {
		log.Printf("failed to refresh VEX file after %d attempts: %v; using old VEX file %q", vexDownloadAttempts, lastErr, vexFilePath)
		return vexFilePath, nil
	}

	return "", fmt.Errorf("failed to download vex report from %q after %d attempts and no local VEX file is available; local scan requires the VEX file: %w", vexReportURL, vexDownloadAttempts, lastErr)
}

// downloadVEXFileOnce downloads the VEX file from the hardcoded URL and saves it to the given path
func downloadVEXFileOnce(vexDirectory string, vexFilePath string) error {
	response, err := http.Get(vexReportURL)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("unexpected status %d: %s", response.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	temporaryFile, err := os.CreateTemp(vexDirectory, "rancher.openvex-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temporary vex file: %w", err)
	}
	temporaryFilePath := temporaryFile.Name()
	defer func() {
		_ = os.Remove(temporaryFilePath)
	}()

	if _, copyErr := io.Copy(temporaryFile, response.Body); copyErr != nil {
		_ = temporaryFile.Close()
		return fmt.Errorf("failed to write vex report content to %q: %w", temporaryFilePath, copyErr)
	}

	if closeErr := temporaryFile.Close(); closeErr != nil {
		return fmt.Errorf("failed to close temporary vex file %q: %w", temporaryFilePath, closeErr)
	}

	if renameErr := os.Rename(temporaryFilePath, vexFilePath); renameErr != nil {
		return fmt.Errorf("failed to place vex report at %q: %w", vexFilePath, renameErr)
	}

	return nil
}

// resolveScanMode determines the scan mode based on the RKE2_PATCHER_CVE_MODE environment variable
func resolveScanMode() (string, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(cveModeEnv)))
	if mode == "" {
		return "cluster", nil
	}

	switch mode {
	case "cluster", "local":
		return mode, nil
	default:
		return "", fmt.Errorf("invalid %s value %q: expected cluster or local", cveModeEnv, mode)
	}
}

func grypeCVEs(image string) ([]string, error) {
	vexFilePath, err := ensureLocalVEXFile()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("grype", image, "-o", "json", "--vex", vexFilePath)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var report struct {
		Matches []struct {
			Vulnerability struct {
				ID       string `json:"id"`
				Severity string `json:"severity"`
			} `json:"vulnerability"`
		} `json:"matches"`
	}

	if err := json.Unmarshal(output, &report); err != nil {
		return nil, err
	}

	return dedupeCVEs(func(appendCVE func(string)) {
		for _, match := range report.Matches {
			severity := strings.ToUpper(strings.TrimSpace(match.Vulnerability.Severity))
			if severity != "CRITICAL" && severity != "HIGH" {
				continue
			}
			appendCVE(match.Vulnerability.ID)
		}
	}), nil
}

// dedupeCVEs collects CVE IDs from the function and returns a deduplicated, sorted list of CVE IDs
func dedupeCVEs(visitor func(func(string))) []string {
	set := make(map[string]struct{})
	visitor(func(value string) {
		id := strings.TrimSpace(value)
		if id == "" {
			return
		}
		set[id] = struct{}{}
	})

	items := make([]string, 0, len(set))
	for id := range set {
		items = append(items, id)
	}
	sort.Strings(items)

	return items
}
