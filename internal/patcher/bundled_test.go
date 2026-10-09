package patcher

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Layouts copied from the charts bundled with RKE2 v1.35.3+rke2r3 (and nginx from a Prime cluster)
const (
	corednsChartValues = `
image:
  repository: rancher/hardened-coredns
  tag: "v1.14.2-build20260310"
autoscaler:
  image:
    repository: rancher/hardened-cluster-autoscaler
    tag: "v1.10.3-build20260206"
nodelocal:
  image:
    repository: rancher/hardened-dns-node-cache
    tag: "1.26.7-build20260310"
`
	canalChartValues = `
flannel:
  image:
    repository: rancher/hardened-flannel
    tag: v0.28.2-build20260327
calico:
  cniImage:
    repository: rancher/hardened-calico
    tag: v3.31.4-build20260327
  nodeImage:
    repository: rancher/hardened-calico
    tag: v3.31.4-build20260327
  flexvolImage:
    repository: rancher/hardened-calico
    tag: v3.31.4-build20260327
  kubeControllerImage:
    repository: rancher/hardened-calico
    tag: v3.31.4-build20260327
`
	nginxChartValues = `
controller:
  image:
    repository: rancher/nginx-ingress-controller
    tag: "v1.14.5-hardened2"
    primeTag: "v1.14.5-prime3"
`
	snapshotChartValues = `
controller:
  image:
    repository: rancher/hardened-snapshot-controller
    tag: "v8.4.0-build20260205"
`
)

func chartValues(t *testing.T, raw string) map[string]any {
	t.Helper()
	values := map[string]any{}
	if err := yaml.Unmarshal([]byte(raw), &values); err != nil {
		t.Fatal(err)
	}
	return values
}

func TestBundledImageTag(t *testing.T) {
	cases := []struct {
		component, chart, values, repository, want string
	}{
		{"rke2-coredns", "rke2-coredns", corednsChartValues, "rancher/hardened-coredns", "v1.14.2-build20260310"},
		{"rke2-coredns-cluster-autoscaler", "rke2-coredns", corednsChartValues, "rancher/hardened-cluster-autoscaler", "v1.10.3-build20260206"},
		{"rke2-canal-calico", "rke2-canal", canalChartValues, "rancher/hardened-calico", "v3.31.4-build20260327"},
		{"rke2-canal-flannel", "rke2-canal", canalChartValues, "rancher/hardened-flannel", "v0.28.2-build20260327"},
		{"rke2-ingress-nginx", "rke2-ingress-nginx", nginxChartValues, "rancher/nginx-ingress-controller", "v1.14.5-prime3"},
		{"rke2-snapshot-controller", "rke2-snapshot-controller", snapshotChartValues, "rancher/hardened-snapshot-controller", "v8.4.0-build20260205"},
	}
	for _, tc := range cases {
		got, err := BundledImageTag(tc.component, tc.chart, chartValues(t, tc.values), tc.repository)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, err %v; want %q", tc.component, got, err, tc.want)
		}
	}
}

// rke2-dns-node-cache writes image.* into the shared rke2-coredns chart, which is CoreDNS's
// image: the repository check must refuse it instead of returning CoreDNS's tag
func TestBundledImageTagRefusesPathOfAnotherImage(t *testing.T) {
	_, err := BundledImageTag("rke2-dns-node-cache", "rke2-coredns", chartValues(t, corednsChartValues), "rancher/hardened-dns-node-cache")
	if err == nil || !strings.Contains(err.Error(), "rancher/hardened-coredns") {
		t.Fatalf("expected repository mismatch, got %v", err)
	}
}

func TestBundledImageTagMissingPath(t *testing.T) {
	_, err := BundledImageTag("rke2-traefik", "rke2-traefik", chartValues(t, "ports:\n  web: 80\n"), "rancher/hardened-traefik")
	if err == nil || !strings.Contains(err.Error(), "no default value at image.tag") {
		t.Fatalf("expected missing path error, got %v", err)
	}
}

func TestHelmChartConfigAnnotations(t *testing.T) {
	generated, _ := BuildHelmChartConfig("rke2-traefik", "rke2-traefik", "rancher/hardened-traefik", "v3.6.12-build20260409")
	merged, err := MergeHelmChartConfigWithContent(generated, existingWithIntegers)
	if err != nil {
		t.Fatal(err)
	}

	annotated, err := SetHelmChartConfigAnnotation(merged, "patcher.rke2.cattle.io/rke2-traefik", "v1.35.3+rke2r3", false)
	if err != nil {
		t.Fatal(err)
	}
	value, found, err := HelmChartConfigAnnotation(annotated, "patcher.rke2.cattle.io/rke2-traefik")
	if err != nil || !found || value != "v1.35.3+rke2r3" {
		t.Fatalf("annotation = %q found=%v err=%v", value, found, err)
	}
	if present, _, err := CompareGeneratedValues(annotated, mustValues(t, "rke2-traefik", "v3.6.12-build20260409")); err != nil || !present {
		t.Fatalf("annotating changed the values (present=%v err=%v):\n%s", present, err, annotated)
	}

	removed, err := SetHelmChartConfigAnnotation(annotated, "patcher.rke2.cattle.io/rke2-traefik", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := HelmChartConfigAnnotation(removed, "patcher.rke2.cattle.io/rke2-traefik"); found {
		t.Fatal("annotation not removed")
	}
	if !strings.Contains(removed, "replicas: 2") {
		t.Fatalf("removing the annotation lost values:\n%s", removed)
	}
}

func TestSnapshotControllerValuesMatchChart(t *testing.T) {
	paths, err := ImageTagPaths("rke2-snapshot-controller", "rke2-snapshot-controller")
	if err != nil || len(paths) != 1 || strings.Join(paths[0], ".") != "controller.image.tag" {
		t.Fatalf("paths = %v, err %v", paths, err)
	}
}

func mustValues(t *testing.T, component string, tag string) string {
	t.Helper()
	_, values := BuildHelmChartConfig(component, component, "rancher/hardened-traefik", tag)
	return values
}
