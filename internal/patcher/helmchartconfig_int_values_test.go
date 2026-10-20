package patcher

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const existingWithIntegers = `apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: rke2-traefik
  namespace: kube-system
spec:
  valuesContent: |-
    deployment:
      replicas: 2
    ports:
      - 80
      - 443
`

// yaml.v3 decodes integers as int, which runtime.DeepCopyJSON used to panic on
func TestMergeWithIntegerValues(t *testing.T) {
	generated, _ := BuildHelmChartConfig("rke2-traefik", "rke2-traefik", "rancher/hardened-traefik", "v3.3.6-build20260912")

	merged, err := MergeHelmChartConfigWithContent(generated, existingWithIntegers)
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	for _, want := range []string{"replicas: 2", "tag: v3.3.6-build20260912", "- 443"} {
		if !strings.Contains(merged, want) {
			t.Fatalf("merged HelmChartConfig missing %q:\n%s", want, merged)
		}
	}
}

func TestSubtractWithIntegerValues(t *testing.T) {
	generated, generatedValues := BuildHelmChartConfig("rke2-traefik", "rke2-traefik", "rancher/hardened-traefik", "v3.3.6-build20260912")
	merged, err := MergeHelmChartConfigWithContent(generated, existingWithIntegers)
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	reverted, err := SubtractPatcherValuesContent(merged, generatedValues)
	if err != nil {
		t.Fatalf("subtract failed: %v", err)
	}
	if strings.Contains(reverted, "hardened-traefik") || !strings.Contains(reverted, "replicas: 2") {
		t.Fatalf("unexpected reverted HelmChartConfig:\n%s", reverted)
	}
}

// Keys nested exactly 4 spaces deep used to lose their indentation when merging into an
// empty valuesContent. Components have different shapes here (a single image.tag, calico's
// four <name>Image.tag pairs, nginx's primeTag), so this compares the whole parsed tree
// rather than assuming any particular key layout.
func TestMergeIntoEmptyKeepsDeepNesting(t *testing.T) {
	for _, component := range []struct{ name, chart string }{
		{"rke2-coredns-cluster-autoscaler", "rke2-coredns"},
		{"rke2-canal-flannel", "rke2-canal"},
		{"rke2-canal-calico", "rke2-canal"},
		{"rke2-ingress-nginx", "rke2-ingress-nginx"},
		{"rke2-snapshot-controller", "rke2-snapshot-controller"},
	} {
		generated, generatedValues := BuildHelmChartConfig(component.name, component.chart, "rancher/some-image", "v1.2.3-build20260101")
		merged, err := MergeHelmChartConfigWithContent(generated, "")
		if err != nil {
			t.Fatalf("%s: merge failed: %v", component.name, err)
		}

		var want map[string]any
		if err := yaml.Unmarshal([]byte(generatedValues), &want); err != nil {
			t.Fatalf("%s: failed to parse generated values: %v", component.name, err)
		}

		hcc, err := parseSingleHelmChartConfig(merged)
		if err != nil {
			t.Fatalf("%s: failed to parse merged HelmChartConfig: %v", component.name, err)
		}
		var got map[string]any
		if err := yaml.Unmarshal([]byte(hcc.Spec.ValuesContent), &got); err != nil {
			t.Fatalf("%s: failed to parse merged valuesContent: %v", component.name, err)
		}

		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: merging into empty did not preserve nesting:\n got=%#v\nwant=%#v\nmerged=\n%s", component.name, got, want, merged)
		}
	}
}

// rke2-snapshot-controller's patch used to be written to a top-level image.repository/tag,
// which the chart does not read (it reads controller.image.*); the component's pods kept
// running the bundled tag even though image-patch reported success.
func TestSnapshotControllerWritesControllerImageNotTopLevelImage(t *testing.T) {
	generated, _ := BuildHelmChartConfig("rke2-snapshot-controller", "rke2-snapshot-controller", "rancher/hardened-snapshot-controller", "v8.5.0-build20260410")

	hcc, err := parseSingleHelmChartConfig(generated)
	if err != nil {
		t.Fatalf("failed to parse generated HelmChartConfig: %v", err)
	}
	var values map[string]any
	if err := yaml.Unmarshal([]byte(hcc.Spec.ValuesContent), &values); err != nil {
		t.Fatalf("failed to parse valuesContent: %v", err)
	}

	if _, found := values["image"]; found {
		t.Fatalf("patch wrote a top-level image key, which the chart ignores:\n%s", generated)
	}

	controller, ok := values["controller"].(map[string]any)
	if !ok {
		t.Fatalf("expected a controller key, got:\n%s", generated)
	}
	image, ok := controller["image"].(map[string]any)
	if !ok {
		t.Fatalf("expected controller.image, got:\n%s", generated)
	}
	if image["repository"] != "rancher/hardened-snapshot-controller" {
		t.Fatalf("controller.image.repository = %v, want rancher/hardened-snapshot-controller", image["repository"])
	}
	if image["tag"] != "v8.5.0-build20260410" {
		t.Fatalf("controller.image.tag = %v, want v8.5.0-build20260410", image["tag"])
	}
}
