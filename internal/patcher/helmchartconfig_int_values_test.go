package patcher

import (
	"strings"
	"testing"
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

func TestCompareGeneratedValues(t *testing.T) {
	_, generatedValues := BuildHelmChartConfig("rke2-traefik", "rke2-traefik", "rancher/hardened-traefik", "v3.3.6-build20260912")
	generated, _ := BuildHelmChartConfig("rke2-traefik", "rke2-traefik", "rancher/hardened-traefik", "v3.3.6-build20260912")
	merged, err := MergeHelmChartConfigWithContent(generated, existingWithIntegers)
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	present, conflicts, err := CompareGeneratedValues(merged, generatedValues)
	if err != nil || !present || len(conflicts) != 0 {
		t.Fatalf("after merge: present=%v conflicts=%v err=%v", present, conflicts, err)
	}

	present, conflicts, err = CompareGeneratedValues(existingWithIntegers, generatedValues)
	if err != nil || present || len(conflicts) != 0 {
		t.Fatalf("without patch: present=%v conflicts=%v err=%v", present, conflicts, err)
	}

	_, otherValues := BuildHelmChartConfig("rke2-traefik", "rke2-traefik", "rancher/hardened-traefik", "v3.3.4-build20260801")
	present, conflicts, err = CompareGeneratedValues(merged, otherValues)
	if err != nil || present || len(conflicts) != 1 || conflicts[0] != "image.tag" {
		t.Fatalf("different tag: present=%v conflicts=%v err=%v", present, conflicts, err)
	}
}

// Keys nested exactly 4 spaces deep used to lose their indentation when merging into an
// empty valuesContent
func TestMergeIntoEmptyKeepsDeepNesting(t *testing.T) {
	for _, component := range []struct{ name, chart, path string }{
		{"rke2-coredns-cluster-autoscaler", "rke2-coredns", "autoscaler"},
		{"rke2-canal-flannel", "rke2-canal", "flannel"},
		{"rke2-canal-calico", "rke2-canal", "calico"},
		{"rke2-ingress-nginx", "rke2-ingress-nginx", "controller"},
	} {
		generated, generatedValues := BuildHelmChartConfig(component.name, component.chart, "rancher/some-image", "v1.2.3-build20260101")
		merged, err := MergeHelmChartConfigWithContent(generated, "")
		if err != nil {
			t.Fatalf("%s: merge failed: %v", component.name, err)
		}
		present, conflicts, err := CompareGeneratedValues(merged, generatedValues)
		if err != nil || !present || len(conflicts) != 0 {
			t.Fatalf("%s: generated values not preserved (present=%v conflicts=%v err=%v):\n%s", component.name, present, conflicts, err, merged)
		}
	}
}
