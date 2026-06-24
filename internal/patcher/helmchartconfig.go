package patcher

import (
	"fmt"
	"io"
	"strings"

	"dario.cat/mergo"
	helmcontrollerv1 "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/runtime"
	syaml "sigs.k8s.io/yaml"
)

const (
	registryEnv = "RKE2_PATCHER_REGISTRY"

	defaultNamespace    = "kube-system"
	defaultRegistryHost = "registry.rancher.com"
)

// BuildHelmChartConfig generates the file path and content for a HelmChartConfig manifest.
//
// The file name is derived from the target HelmChartConfig/chart name rather than the
// component name so multiple components that patch the same chart (for example
// `rke2-canal-flannel` and `rke2-canal-calico`) converge on the same manifest file and
// can be merged on subsequent patch runs.
func BuildHelmChartConfig(componentName string, defaultChartConfigName string, imageName string, imageTag string) (string, string) {

	repo := imageRepositoryWithoutRegistry(imageName)
	valuesContent := renderValuesContent(componentName, defaultChartConfigName, repo, imageTag)
	content := renderHelmChartConfig(defaultChartConfigName, defaultNamespace, valuesContent)

	return content, valuesContent
}

func MergeHelmChartConfigWithContent(generatedContent string, existingContent string) (string, error) {
	generatedHcc, err := parseSingleHelmChartConfig(generatedContent)
	if err != nil {
		return "", fmt.Errorf("failed to parse generated HelmChartConfig: %w", err)
	}

	targetName := strings.TrimSpace(generatedHcc.Name)
	targetNamespace := strings.TrimSpace(generatedHcc.Namespace)
	if targetName == "" || targetNamespace == "" {
		return "", fmt.Errorf("generated HelmChartConfig is missing metadata.name or metadata.namespace")
	}

	mergedHcc := generatedHcc.DeepCopy()
	var mergedSpec helmcontrollerv1.HelmChartConfigSpec

	// If there's existing content, merge it
	if strings.TrimSpace(existingContent) != "" {
		existingHcc, err := parseSingleHelmChartConfig(existingContent)
		if err != nil {
			return "", fmt.Errorf("failed to parse existing HelmChartConfig: %w", err)
		}

		if strings.TrimSpace(existingHcc.Name) == targetName && strings.TrimSpace(existingHcc.Namespace) == targetNamespace {
			mergedSpec, err = mergeHelmChartConfigSpec(existingHcc.Spec, generatedHcc.Spec)
			if err != nil {
				return "", err
			}
			// Start from the existing object so metadata is preserved
			mergedHcc = existingHcc.DeepCopy()
			mergedHcc.Spec = mergedSpec
		}
	} else {
		// No existing, just normalize the generated spec through the standard merge path
		var err error
		mergedSpec, err = mergeHelmChartConfigSpec(helmcontrollerv1.HelmChartConfigSpec{}, generatedHcc.Spec)
		if err != nil {
			return "", err
		}
		mergedHcc.Spec = mergedSpec
	}

	if strings.TrimSpace(mergedHcc.APIVersion) == "" {
		mergedHcc.APIVersion = "helm.cattle.io/v1"
	}
	if strings.TrimSpace(mergedHcc.Kind) == "" {
		mergedHcc.Kind = "HelmChartConfig"
	}

	// Deindent valuesContent before marshaling to get stable block scalar format
	mergedHcc.Spec.ValuesContent = deindentValuesContent(mergedHcc.Spec.ValuesContent)

	// Render the merged HelmChartConfig from the typed object
	contentBytes, err := syaml.Marshal(mergedHcc)
	if err != nil {
		return "", fmt.Errorf("failed to render HelmChartConfig: %w", err)
	}

	return string(contentBytes), nil
}

// HelmChartConfigIdentityFromContent extracts the metadata.name and metadata.namespace from a HelmChartConfig YAML
func HelmChartConfigIdentityFromContent(content string) (string, string, error) {
	hcc, err := parseSingleHelmChartConfig(content)
	if err != nil {
		return "", "", err
	}

	name := strings.TrimSpace(hcc.Name)
	namespace := strings.TrimSpace(hcc.Namespace)
	if name == "" || namespace == "" {
		return "", "", fmt.Errorf("HelmChartConfig content missing metadata.name or metadata.namespace")
	}

	return name, namespace, nil
}

// parseSingleHelmChartConfig parses a YAML into a proper HelmChartConfig object
func parseSingleHelmChartConfig(content string) (*helmcontrollerv1.HelmChartConfig, error) {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	for {
		var obj map[string]any
		err := decoder.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(obj) == 0 {
			continue
		}

		raw, err := yaml.Marshal(obj)
		if err != nil {
			return nil, err
		}

		var hcc helmcontrollerv1.HelmChartConfig
		if err := syaml.Unmarshal(raw, &hcc); err != nil {
			return nil, err
		}

		if strings.EqualFold(strings.TrimSpace(hcc.Kind), "HelmChartConfig") {
			return &hcc, nil
		}
	}

	return nil, fmt.Errorf("no HelmChartConfig found")
}

// mergeHelmChartConfigSpec takes two HelmChartConfigSpec objects and merges them
func mergeHelmChartConfigSpec(existing helmcontrollerv1.HelmChartConfigSpec, generated helmcontrollerv1.HelmChartConfigSpec) (helmcontrollerv1.HelmChartConfigSpec, error) {
	merged := existing
	combinedValues, err := mergeValuesContent(existing.ValuesContent, generated.ValuesContent)
	if err != nil {
		return helmcontrollerv1.HelmChartConfigSpec{}, err
	}
	merged.ValuesContent = combinedValues

	generated.ValuesContent = ""
	if err := mergo.Merge(&merged, generated, mergo.WithOverride); err != nil {
		return helmcontrollerv1.HelmChartConfigSpec{}, fmt.Errorf("failed to merge HelmChartConfig spec: %w", err)
	}

	return merged, nil
}

func mergeMapsWithOverride(base map[string]any, overlay map[string]any) (map[string]any, error) {
	result := runtime.DeepCopyJSON(base)
	if result == nil {
		result = map[string]any{}
	}

	if overlay != nil {
		if err := mergo.Merge(&result, overlay, mergo.WithOverride); err != nil {
			return nil, fmt.Errorf("failed to merge overlay values: %w", err)
		}
	}

	return result, nil
}

// ensureProperIndentation adds 4-space indentation to each line that doesn't already have it.
// This preserves any existing indentation (for nested keys) and comments while ensuring the
// top-level content is indented for block scalar display.
func ensureProperIndentation(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	for i, line := range lines {
		if len(line) > 0 && !strings.HasPrefix(line, "    ") {
			lines[i] = "    " + line
		}
	}
	return strings.Join(lines, "\n")
}

// isProperlyIndented checks if all non-empty lines start with at least 4 spaces.
// This indicates the content is already formatted for use as a YAML block scalar value.
func isProperlyIndented(content string) bool {
	if strings.TrimSpace(content) == "" {
		return true // Empty is considered "properly indented"
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, "    ") {
			return false
		}
	}
	return true
}

// normalizeValuesContent parses YAML content, re-marshals it, and adds proper indentation.
func normalizeValuesContent(content string) (string, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", nil
	}

	var values any
	if err := yaml.Unmarshal([]byte(trimmed), &values); err != nil {
		return "", fmt.Errorf("failed to parse valuesContent: %w", err)
	}

	b, err := yaml.Marshal(values)
	if err != nil {
		return "", err
	}

	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	for i, line := range lines {
		lines[i] = "    " + line
	}

	return strings.Join(lines, "\n"), nil
}

// deindentValuesContent removes one top-level indentation layer (4 spaces).
// This allows syaml.Marshal to emit a stable block scalar format (|-) not |2-.
func deindentValuesContent(content string) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}

	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	allIndented := true
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, "    ") {
			allIndented = false
			break
		}
	}

	if !allIndented {
		return content
	}

	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines[i] = strings.TrimPrefix(line, "    ")
	}

	return strings.Join(lines, "\n")
}

func mergeValuesContent(existing string, incoming string) (string, error) {
	existingTrimmed := strings.TrimSpace(existing)
	incomingTrimmed := strings.TrimSpace(incoming)

	if existingTrimmed == "" {
		// If incoming already has patcher comments, preserve them by adding indentation if needed.
		if strings.Contains(incoming, "# change made by rke2-patcher") {
			return ensureProperIndentation(incoming), nil
		}
		// If properly indented, preserve as-is. Otherwise normalize.
		if isProperlyIndented(incoming) {
			return incoming, nil
		}
		return normalizeValuesContent(incoming)
	}
	if incomingTrimmed == "" {
		// Same for existing: preserve patcher comments with proper indentation.
		if strings.Contains(existing, "# change made by rke2-patcher") {
			return ensureProperIndentation(existing), nil
		}
		// If properly indented, preserve as-is. Otherwise normalize.
		if isProperlyIndented(existing) {
			return existing, nil
		}
		return normalizeValuesContent(existing)
	}

	var existingValues any
	if err := yaml.Unmarshal([]byte(existing), &existingValues); err != nil {
		return "", fmt.Errorf("failed to parse existing valuesContent: %w", err)
	}

	var incomingValues any
	if err := yaml.Unmarshal([]byte(incoming), &incomingValues); err != nil {
		return "", fmt.Errorf("failed to parse generated valuesContent: %w", err)
	}

	mergedValues := runtime.DeepCopyJSONValue(incomingValues)
	existingMap, existingIsMap := existingValues.(map[string]any)
	incomingMap, incomingIsMap := incomingValues.(map[string]any)
	if existingIsMap && incomingIsMap {
		mergedMap, err := mergeMapsWithOverride(existingMap, incomingMap)
		if err != nil {
			return "", fmt.Errorf("failed to merge valuesContent maps: %w", err)
		}
		mergedValues = mergedMap
	}

	b, err := yaml.Marshal(mergedValues)
	if err != nil {
		return "", err
	}

	// Indent each line by 4 spaces to match the original style
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	for i, line := range lines {
		lines[i] = "    " + line
	}
	return strings.Join(lines, "\n"), nil
}

func SubtractPatcherValuesContent(existingFileContent, generatedValuesContent string) (string, error) {
	existingHcc, err := parseSingleHelmChartConfig(existingFileContent)
	if err != nil {
		return "", fmt.Errorf("failed to parse existing HelmChartConfig: %w", err)
	}

	existingValuesStr := strings.TrimSpace(existingHcc.Spec.ValuesContent)
	if existingValuesStr == "" {
		return existingFileContent, nil
	}

	trimmedGenerated := strings.TrimSpace(generatedValuesContent)
	if trimmedGenerated == "" {
		return existingFileContent, nil
	}

	var generatedValues map[string]any
	if err := yaml.Unmarshal([]byte(trimmedGenerated), &generatedValues); err != nil {
		return "", fmt.Errorf("failed to parse generated valuesContent: %w", err)
	}

	var existingValues map[string]any
	if err := yaml.Unmarshal([]byte(existingValuesStr), &existingValues); err != nil {
		return "", fmt.Errorf("failed to parse existing valuesContent: %w", err)
	}

	resultValues := deepSubtractMap(existingValues, generatedValues)

	updatedSpec := existingHcc.Spec
	if len(resultValues) == 0 {
		updatedSpec.ValuesContent = ""
	} else {
		b, err := yaml.Marshal(resultValues)
		if err != nil {
			return "", fmt.Errorf("failed to serialize updated valuesContent: %w", err)
		}
		// Indent each line by 4 spaces to match the original style
		lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		for i, line := range lines {
			lines[i] = "    " + line
		}
		updatedSpec.ValuesContent = strings.Join(lines, "\n")
	}

	existingHcc.Spec = updatedSpec

	// Deindent valuesContent before marshaling to get stable block scalar format
	existingHcc.Spec.ValuesContent = deindentValuesContent(existingHcc.Spec.ValuesContent)

	// Render the updated HelmChartConfig from the typed object
	contentBytes, err := syaml.Marshal(existingHcc)
	if err != nil {
		return "", fmt.Errorf("failed to render HelmChartConfig: %w", err)
	}

	return string(contentBytes), nil
}

func deepSubtractMap(base, toRemove map[string]any) map[string]any {
	result := map[string]any{}
	if base != nil {
		result = runtime.DeepCopyJSON(base)
	}
	for key, removeValue := range toRemove {
		existingValue, found := result[key]
		if !found {
			continue
		}

		removeMap, removeIsMap := removeValue.(map[string]any)
		existingMap, existingIsMap := existingValue.(map[string]any)

		if removeIsMap && existingIsMap {
			subtracted := deepSubtractMap(existingMap, removeMap)
			if len(subtracted) == 0 {
				delete(result, key)
			} else {
				result[key] = subtracted
			}
		} else {
			delete(result, key)
		}
	}
	return result
}

// renderHelmChartConfig generates the content of a HelmChartConfig manifest for the given component, chart, and image details
func renderHelmChartConfig(chartName string, namespace string, valuesContent string) string {
	return fmt.Sprintf(`apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: %s
  namespace: %s
spec:
  valuesContent: |-
%s
`, chartName, namespace, valuesContent)
}

// renderValuesContent generates the valuesContent block for the HelmChartConfig based on the component and chart names
func renderValuesContent(componentName string, chartName string, imageName string, imageTag string) string {
	if strings.EqualFold(chartName, "rke2-ingress-nginx") {
		return fmt.Sprintf(`    controller: # change made by rke2-patcher
      image: # change made by rke2-patcher
        repository: %s # change made by rke2-patcher
        primeTag: %s # change made by rke2-patcher`, imageName, imageTag)
	}

	if strings.EqualFold(componentName, "rke2-canal-calico") {
		return fmt.Sprintf("    calico: # change made by rke2-patcher\n"+
			"      cniImage: # change made by rke2-patcher\n"+
			"        repository: %s # change made by rke2-patcher\n"+
			"        tag: %s # change made by rke2-patcher\n"+
			"      nodeImage: # change made by rke2-patcher\n"+
			"        repository: %s # change made by rke2-patcher\n"+
			"        tag: %s # change made by rke2-patcher\n"+
			"      flexvolImage: # change made by rke2-patcher\n"+
			"        repository: %s # change made by rke2-patcher\n"+
			"        tag: %s # change made by rke2-patcher\n"+
			"      kubeControllerImage: # change made by rke2-patcher\n"+
			"        repository: %s # change made by rke2-patcher\n"+
			"        tag: %s # change made by rke2-patcher",
			imageName, imageTag, imageName, imageTag, imageName, imageTag, imageName, imageTag)
	}

	if strings.EqualFold(componentName, "rke2-canal-flannel") {
		return fmt.Sprintf(`    flannel: # change made by rke2-patcher
      image: # change made by rke2-patcher
        repository: %s # change made by rke2-patcher
        tag: %s # change made by rke2-patcher`, imageName, imageTag)
	}

	if strings.EqualFold(componentName, "rke2-flannel") {
		return fmt.Sprintf(`    flannel:
      image:
        repository: %s
        tag: %s`, imageName, imageTag)
	}

	if strings.EqualFold(componentName, "rke2-coredns-cluster-autoscaler") {
		return fmt.Sprintf(`    autoscaler: # change made by rke2-patcher
      image: # change made by rke2-patcher
        repository: %s # change made by rke2-patcher
        tag: %s # change made by rke2-patcher`, imageName, imageTag)
	}

	return fmt.Sprintf(`    image: # change made by rke2-patcher
      repository: %s # change made by rke2-patcher
      tag: %s # change made by rke2-patcher`, imageName, imageTag)
}

func imageRepositoryWithoutRegistry(imageName string) string {
	parts := strings.Split(imageName, "/")
	if len(parts) < 2 {
		return imageName
	}

	first := strings.ToLower(parts[0])
	hasRegistryPrefix := strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost"
	if !hasRegistryPrefix {
		return imageName
	}

	return strings.Join(parts[1:], "/")
}
