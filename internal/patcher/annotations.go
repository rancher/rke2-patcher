package patcher

import (
	"fmt"

	syaml "sigs.k8s.io/yaml"
)

// HelmChartConfigAnnotation returns an annotation of a HelmChartConfig manifest
func HelmChartConfigAnnotation(content string, key string) (string, bool, error) {
	hcc, err := parseSingleHelmChartConfig(content)
	if err != nil {
		return "", false, err
	}
	value, found := hcc.Annotations[key]
	return value, found, nil
}

// SetHelmChartConfigAnnotation sets (or, with remove, deletes) an annotation of a
// HelmChartConfig manifest and renders it the same way MergeHelmChartConfigWithContent does
func SetHelmChartConfigAnnotation(content string, key string, value string, remove bool) (string, error) {
	hcc, err := parseSingleHelmChartConfig(content)
	if err != nil {
		return "", err
	}

	if remove {
		delete(hcc.Annotations, key)
		if len(hcc.Annotations) == 0 {
			hcc.Annotations = nil
		}
	} else {
		if hcc.Annotations == nil {
			hcc.Annotations = map[string]string{}
		}
		hcc.Annotations[key] = value
	}

	hcc.Spec.ValuesContent = deindentValuesContent(hcc.Spec.ValuesContent)
	rendered, err := syaml.Marshal(hcc)
	if err != nil {
		return "", fmt.Errorf("failed to render HelmChartConfig: %w", err)
	}
	return string(rendered), nil
}
