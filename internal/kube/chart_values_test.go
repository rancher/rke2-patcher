package kube

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"testing"
)

func chartArchive(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestChartValuesFromContentReadsTopLevelValues(t *testing.T) {
	content := chartArchive(t, map[string]string{
		"rke2-coredns/Chart.yaml":             "name: rke2-coredns\n",
		"rke2-coredns/charts/sub/values.yaml": "image:\n  tag: wrong\n",
		"rke2-coredns/values.yaml":            "image:\n  repository: rancher/hardened-coredns\n  tag: \"v1.14.2-build20260416\"\n",
	})

	values, err := ChartValuesFromContent(content)
	if err != nil {
		t.Fatal(err)
	}
	image, _ := values["image"].(map[string]any)
	if image["tag"] != "v1.14.2-build20260416" {
		t.Fatalf("unexpected values: %v", values)
	}
}

func TestChartValuesFromContentEmpty(t *testing.T) {
	if _, err := ChartValuesFromContent(""); err == nil {
		t.Fatal("expected an error for an empty chartContent")
	}
}
