package patcher

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// CompareGeneratedValues checks the generated values against the valuesContent of an
// existing HelmChartConfig. It reports whether every generated leaf is already present
// with the same value, and lists the leaf paths the existing object sets to a different value.
func CompareGeneratedValues(existingContent string, generatedValuesContent string) (bool, []string, error) {
	var generated map[string]any
	if err := yaml.Unmarshal([]byte(strings.TrimSpace(generatedValuesContent)), &generated); err != nil {
		return false, nil, fmt.Errorf("failed to parse generated valuesContent: %w", err)
	}

	existing := map[string]any{}
	if strings.TrimSpace(existingContent) != "" {
		hcc, err := parseSingleHelmChartConfig(existingContent)
		if err != nil {
			return false, nil, fmt.Errorf("failed to parse existing HelmChartConfig: %w", err)
		}
		if values := strings.TrimSpace(hcc.Spec.ValuesContent); values != "" {
			if err := yaml.Unmarshal([]byte(values), &existing); err != nil {
				return false, nil, fmt.Errorf("failed to parse existing valuesContent: %w", err)
			}
		}
	}

	allPresent := true
	var conflicts []string
	compareLeaves(existing, generated, "", &allPresent, &conflicts)
	sort.Strings(conflicts)

	return allPresent, conflicts, nil
}

func compareLeaves(existing map[string]any, generated map[string]any, prefix string, allPresent *bool, conflicts *[]string) {
	for key, generatedValue := range generated {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}

		existingValue, found := existing[key]
		generatedMap, generatedIsMap := generatedValue.(map[string]any)
		if generatedIsMap {
			existingMap, existingIsMap := existingValue.(map[string]any)
			if !found || !existingIsMap {
				if found {
					*conflicts = append(*conflicts, path)
				}
				*allPresent = false
				continue
			}
			compareLeaves(existingMap, generatedMap, path, allPresent, conflicts)
			continue
		}

		if !found {
			*allPresent = false
			continue
		}
		if fmt.Sprint(existingValue) != fmt.Sprint(generatedValue) {
			*allPresent = false
			*conflicts = append(*conflicts, path)
		}
	}
}
