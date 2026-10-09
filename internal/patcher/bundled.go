package patcher

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const tagSentinel = "rke2-patcher-tag-sentinel"

// ImageTagPaths returns the chart value paths the patcher writes the image tag to for a
// component (several for rke2-canal-calico). It is derived from the generated values so it
// can never disagree with what the patcher writes.
func ImageTagPaths(componentName string, chartName string) ([][]string, error) {
	values := map[string]any{}
	if err := yaml.Unmarshal([]byte(renderValuesContent(componentName, chartName, "repository", tagSentinel)), &values); err != nil {
		return nil, fmt.Errorf("failed to parse generated values for %s: %w", componentName, err)
	}

	var paths [][]string
	collectSentinelPaths(values, nil, &paths)
	sort.Slice(paths, func(i, j int) bool { return strings.Join(paths[i], ".") < strings.Join(paths[j], ".") })
	if len(paths) == 0 {
		return nil, fmt.Errorf("no image tag path found in generated values for %s", componentName)
	}
	return paths, nil
}

func collectSentinelPaths(values map[string]any, prefix []string, paths *[][]string) {
	for key, value := range values {
		path := append(append([]string(nil), prefix...), key)
		switch typed := value.(type) {
		case map[string]any:
			collectSentinelPaths(typed, path, paths)
		case string:
			if typed == tagSentinel {
				*paths = append(*paths, path)
			}
		}
	}
}

// BundledImageTag reads the bundled image tag of a component from its chart's default
// values, at the same paths the patcher writes. It checks that the repository next to each
// tag is the component's repository, so a patcher path that points at another image of a
// shared chart is reported instead of silently returning the wrong baseline.
func BundledImageTag(componentName string, chartName string, chartValues map[string]any, repository string) (string, error) {
	paths, err := ImageTagPaths(componentName, chartName)
	if err != nil {
		return "", err
	}

	expectedRepository := imageRepositoryWithoutRegistry(repository)
	bundledTag := ""
	for _, path := range paths {
		tag, found := lookupString(chartValues, path)
		if !found || tag == "" {
			return "", fmt.Errorf("chart %s has no default value at %s", chartName, strings.Join(path, "."))
		}

		repositoryPath := append(append([]string(nil), path[:len(path)-1]...), "repository")
		chartRepository, _ := lookupString(chartValues, repositoryPath)
		if imageRepositoryWithoutRegistry(chartRepository) != expectedRepository {
			return "", fmt.Errorf("chart %s sets %s to %q, not %q: the patcher would override another image", chartName, strings.Join(repositoryPath, "."), chartRepository, expectedRepository)
		}

		if bundledTag != "" && tag != bundledTag {
			return "", fmt.Errorf("chart %s bundles different tags for %s (%s and %s)", chartName, componentName, bundledTag, tag)
		}
		bundledTag = tag
	}

	return bundledTag, nil
}

// AppliedImageTag reads the image tag a HelmChartConfig currently sets for a component, at
// the paths the patcher writes. It returns false if the values are not set.
func AppliedImageTag(content string, componentName string, chartName string) (string, bool, error) {
	if strings.TrimSpace(content) == "" {
		return "", false, nil
	}
	hcc, err := parseSingleHelmChartConfig(content)
	if err != nil {
		return "", false, err
	}
	values := map[string]any{}
	if err := yaml.Unmarshal([]byte(hcc.Spec.ValuesContent), &values); err != nil {
		return "", false, fmt.Errorf("failed to parse valuesContent: %w", err)
	}

	paths, err := ImageTagPaths(componentName, chartName)
	if err != nil {
		return "", false, err
	}
	tag, found := lookupString(values, paths[0])
	return tag, found && tag != "", nil
}

func lookupString(values map[string]any, path []string) (string, bool) {
	var current any = values
	for _, key := range path {
		asMap, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		if current, ok = asMap[key]; !ok {
			return "", false
		}
	}
	if current == nil {
		return "", false
	}
	return strings.TrimSpace(fmt.Sprint(current)), true
}
