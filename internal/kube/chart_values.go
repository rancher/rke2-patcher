package kube

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// BundledChartValues returns the default values.yaml of the chart RKE2 bundles for a
// HelmChart. RKE2 embeds the chart archive in spec.chartContent, and replaces it on upgrade,
// so these values always describe the images of the running RKE2 release.
func BundledChartValues(chartName string) (map[string]any, error) {
	charts, err := ListHelmChartsByIdentity(chartName, "kube-system")
	if err != nil {
		return nil, err
	}
	if len(charts) == 0 {
		return nil, fmt.Errorf("HelmChart kube-system/%s not found", chartName)
	}

	var helmChart struct {
		Spec struct {
			ChartContent string `json:"chartContent"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(charts[0].Content), &helmChart); err != nil {
		return nil, fmt.Errorf("failed to parse HelmChart kube-system/%s: %w", chartName, err)
	}

	return ChartValuesFromContent(helmChart.Spec.ChartContent)
}

// ChartValuesFromContent extracts the top-level values.yaml from a base64 encoded chart archive
func ChartValuesFromContent(chartContent string) (map[string]any, error) {
	if strings.TrimSpace(chartContent) == "" {
		return nil, fmt.Errorf("HelmChart has no embedded chart (spec.chartContent is empty)")
	}

	archive, err := base64.StdEncoding.DecodeString(strings.TrimSpace(chartContent))
	if err != nil {
		return nil, fmt.Errorf("failed to decode chartContent: %w", err)
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("failed to decompress chartContent: %w", err)
	}
	defer gz.Close()

	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("chartContent has no values.yaml")
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read chartContent: %w", err)
		}

		// The top-level chart's values are at <chart>/values.yaml; subcharts are deeper
		if strings.Count(header.Name, "/") != 1 || !strings.HasSuffix(header.Name, "/values.yaml") {
			continue
		}

		raw, err := io.ReadAll(reader)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", header.Name, err)
		}
		values := map[string]any{}
		if err := yaml.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", header.Name, err)
		}
		return values, nil
	}
}

// ListImagePatchNames lists the ImagePatch objects, which means the controller mode is in use.
// It returns nothing when the ImagePatch CRD is not installed.
func ListImagePatchNames() ([]string, error) {
	dynamicClient, err := kubeDynamicClient()
	if err != nil {
		return nil, err
	}

	gvr := schema.GroupVersionResource{Group: "patcher.rke2.cattle.io", Version: "v1alpha1", Resource: "imagepatches"}
	list, err := dynamicClient.Resource(gvr).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list ImagePatch objects: %w", err)
	}

	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		names = append(names, item.GetName())
	}
	return names, nil
}
