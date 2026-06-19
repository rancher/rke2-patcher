package patcher

import (
	"fmt"
	"io"
	"strings"

	"dario.cat/mergo"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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
	generatedDoc, err := parseSingleHelmChartConfig(generatedContent)
	if err != nil {
		return "", fmt.Errorf("failed to parse generated HelmChartConfig: %w", err)
	}

	targetName := strings.TrimSpace(generatedDoc.GetName())
	targetNamespace := strings.TrimSpace(generatedDoc.GetNamespace())
	if targetName == "" || targetNamespace == "" {
		return "", fmt.Errorf("generated HelmChartConfig is missing metadata.name or metadata.namespace")
	}

	mergedSpec := map[string]any{}
	spec, found, err := findMatchingSpecInContent(existingContent, targetName, targetNamespace)
	if err != nil {
		return "", err
	}
	if found {
		mergedSpec, err = mergeMapsWithOverride(mergedSpec, spec)
		if err != nil {
			return "", err
		}
	}

	generatedSpec, found, err := unstructured.NestedMap(generatedDoc.Object, "spec")
	if err != nil {
		return "", fmt.Errorf("failed to parse generated HelmChartConfig spec: %w", err)
	}
	if !found || generatedSpec == nil {
		generatedSpec = map[string]any{}
	}

	existingValues, hasExistingValues, err := unstructured.NestedString(mergedSpec, "valuesContent")
	if err != nil {
		return "", fmt.Errorf("failed to parse existing valuesContent: %w", err)
	}
	newValues, hasNewValues, err := unstructured.NestedString(generatedSpec, "valuesContent")
	if err != nil {
		return "", fmt.Errorf("failed to parse generated valuesContent: %w", err)
	}
	if hasExistingValues && hasNewValues {
		combinedValues, err := mergeValuesContent(existingValues, newValues)
		if err != nil {
			return "", err
		}
		generatedSpec["valuesContent"] = combinedValues
	}

	mergedSpec, err = mergeMapsWithOverride(mergedSpec, generatedSpec)
	if err != nil {
		return "", err
	}

	mergedDoc := generatedDoc.DeepCopy()
	if err := unstructured.SetNestedMap(mergedDoc.Object, mergedSpec, "spec"); err != nil {
		return "", fmt.Errorf("failed setting merged HelmChartConfig spec: %w", err)
	}
	if strings.TrimSpace(mergedDoc.GetAPIVersion()) == "" {
		mergedDoc.SetAPIVersion("helm.cattle.io/v1")
	}
	if strings.TrimSpace(mergedDoc.GetKind()) == "" {
		mergedDoc.SetKind("HelmChartConfig")
	}

	// Instead of marshaling the unstructured object (which can cause apiversion duplication),
	// extract the merged spec and use the string template for output.
	name := strings.TrimSpace(mergedDoc.GetName())
	namespace := strings.TrimSpace(mergedDoc.GetNamespace())
	valuesContent, _, _ := unstructured.NestedString(mergedSpec, "valuesContent")
	return renderHelmChartConfig(name, namespace, valuesContent), nil
}

func HelmChartConfigIdentityFromContent(content string) (string, string, error) {
	doc, err := parseSingleHelmChartConfig(content)
	if err != nil {
		return "", "", err
	}

	name := strings.TrimSpace(doc.GetName())
	namespace := strings.TrimSpace(doc.GetNamespace())
	if name == "" || namespace == "" {
		return "", "", fmt.Errorf("HelmChartConfig content missing metadata.name or metadata.namespace")
	}

	return name, namespace, nil
}

func parseSingleHelmChartConfig(content string) (*unstructured.Unstructured, error) {
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

		doc := &unstructured.Unstructured{Object: obj}
		if strings.EqualFold(strings.TrimSpace(doc.GetKind()), "HelmChartConfig") {
			return doc, nil
		}
	}

	return nil, fmt.Errorf("no HelmChartConfig document found")
}

func findMatchingSpecInContent(content string, targetName string, targetNamespace string) (map[string]any, bool, error) {
	decoder := yaml.NewDecoder(strings.NewReader(content))

	for {
		var obj map[string]any
		err := decoder.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, fmt.Errorf("failed parsing HelmChartConfig content: %w", err)
		}
		if len(obj) == 0 {
			continue
		}
		doc := &unstructured.Unstructured{Object: obj}

		if !strings.EqualFold(strings.TrimSpace(doc.GetKind()), "HelmChartConfig") {
			continue
		}

		name := strings.TrimSpace(doc.GetName())
		namespace := strings.TrimSpace(doc.GetNamespace())

		if name == targetName && namespace == targetNamespace {
			spec, found, err := unstructured.NestedMap(doc.Object, "spec")
			if err != nil {
				return nil, false, fmt.Errorf("failed parsing HelmChartConfig spec: %w", err)
			}
			if !found || spec == nil {
				return map[string]any{}, true, nil
			}
			return spec, true, nil
		}
	}

	return nil, false, nil
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

func mergeValuesContent(existing string, incoming string) (string, error) {
	existingTrimmed := strings.TrimSpace(existing)
	incomingTrimmed := strings.TrimSpace(incoming)

	if existingTrimmed == "" {
		return incoming, nil
	}
	if incomingTrimmed == "" {
		return existing, nil
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
	existingDoc, err := parseSingleHelmChartConfig(existingFileContent)
	if err != nil {
		return "", fmt.Errorf("failed to parse existing HelmChartConfig: %w", err)
	}
	existingSpec, found, err := unstructured.NestedMap(existingDoc.Object, "spec")
	if err != nil {
		return "", fmt.Errorf("failed to parse existing HelmChartConfig spec: %w", err)
	}
	if !found || existingSpec == nil {
		existingSpec = map[string]any{}
	}

	existingValuesStr, hasExisting, err := unstructured.NestedString(existingSpec, "valuesContent")
	if err != nil {
		return "", fmt.Errorf("failed to parse existing valuesContent: %w", err)
	}
	if !hasExisting || strings.TrimSpace(existingValuesStr) == "" {
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

	updatedSpec := map[string]any{}
	if existingSpec != nil {
		updatedSpec = runtime.DeepCopyJSON(existingSpec)
	}
	if len(resultValues) == 0 {
		delete(updatedSpec, "valuesContent")
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
		updatedSpec["valuesContent"] = strings.Join(lines, "\n")
	}

	// Instead of marshaling the unstructured object (which can cause apiversion duplication),
	// extract the updated spec and use the string template for output.
	name := strings.TrimSpace(existingDoc.GetName())
	namespace := strings.TrimSpace(existingDoc.GetNamespace())
	valuesContent, _, _ := unstructured.NestedString(updatedSpec, "valuesContent")
	return renderHelmChartConfig(name, namespace, valuesContent), nil
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
