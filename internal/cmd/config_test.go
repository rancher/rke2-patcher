package cmd

import (
	"testing"
)

func TestCollectConfigEntries_Defaults(t *testing.T) {
	t.Setenv(registryEnvName, "")
	t.Setenv(cveModeEnvName, "")
	t.Setenv(cveNamespaceEnvName, "")
	t.Setenv(cveScannerImageEnvName, "")
	t.Setenv(cveJobTimeoutEnvName, "")

	entries, err := collectConfigEntries()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	registry := configEntryByKey(entries, "registry")
	if registry.Effective != "https://registry.rancher.com" {
		t.Fatalf("unexpected default registry effective value: %q", registry.Effective)
	}
	if registry.Source != "default" {
		t.Fatalf("unexpected default registry source: %q", registry.Source)
	}
	if registry.EnvVar != registryEnvName {
		t.Fatalf("unexpected default registry env var: %q", registry.EnvVar)
	}

	scannerMode := configEntryByKey(entries, "scanner_mode")
	if scannerMode.Effective != "cluster" {
		t.Fatalf("unexpected default scanner mode: %q", scannerMode.Effective)
	}

	cveNamespace := configEntryByKey(entries, "cve_namespace")
	if cveNamespace.Effective != "rke2-patcher" {
		t.Fatalf("unexpected default cve namespace: %q", cveNamespace.Effective)
	}
	if cveNamespace.EnvVar != cveNamespaceEnvName {
		t.Fatalf("unexpected default cve namespace env var: %q", cveNamespace.EnvVar)
	}

	stateConfigmap := configEntryByKey(entries, "rke2_patcher_state_configmap")
	if stateConfigmap.EnvVar != "n/a" {
		t.Fatalf("unexpected state configmap env var: %q", stateConfigmap.EnvVar)
	}

	stateNamespace := configEntryByKey(entries, "rke2_patcher_state_namespace")
	if stateNamespace.Key != "" {
		t.Fatalf("expected rke2_patcher_state_namespace to be hidden, got: %#v", stateNamespace)
	}

	stateKey := configEntryByKey(entries, "rke2_patcher_state_key")
	if stateKey.Key != "" {
		t.Fatalf("expected rke2_patcher_state_key to be hidden, got: %#v", stateKey)
	}

	helmNamespace := configEntryByKey(entries, "helm_namespace")
	if helmNamespace.Key != "" {
		t.Fatalf("expected helm_namespace to be hidden, got: %#v", helmNamespace)
	}
}

func TestCollectConfigEntries_Overrides(t *testing.T) {
	t.Setenv(registryEnvName, "mirror.local:5000")
	t.Setenv(cveModeEnvName, "local")
	t.Setenv(cveNamespaceEnvName, "sec-scan")
	t.Setenv(cveScannerImageEnvName, "scanner:1.2.3")
	t.Setenv(cveJobTimeoutEnvName, "11m")

	entries, err := collectConfigEntries()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	registry := configEntryByKey(entries, "registry")
	if registry.Effective != "https://mirror.local:5000" {
		t.Fatalf("unexpected overridden registry effective value: %q", registry.Effective)
	}
	if registry.Source != registryEnvName {
		t.Fatalf("unexpected registry source: %q", registry.Source)
	}
	if registry.EnvVar != registryEnvName {
		t.Fatalf("unexpected registry env var: %q", registry.EnvVar)
	}

	scannerMode := configEntryByKey(entries, "scanner_mode")
	if scannerMode.Effective != "local" {
		t.Fatalf("unexpected scanner mode effective value: %q", scannerMode.Effective)
	}
	if scannerMode.Source != cveModeEnvName {
		t.Fatalf("unexpected scanner mode source: %q", scannerMode.Source)
	}
	if scannerMode.EnvVar != cveModeEnvName {
		t.Fatalf("unexpected scanner mode env var: %q", scannerMode.EnvVar)
	}

	cveNamespace := configEntryByKey(entries, "cve_namespace")
	if cveNamespace.Effective != "sec-scan" {
		t.Fatalf("unexpected overridden cve namespace: %q", cveNamespace.Effective)
	}

	stateNamespace := configEntryByKey(entries, "rke2_patcher_state_namespace")
	if stateNamespace.Key != "" {
		t.Fatalf("expected rke2_patcher_state_namespace to be hidden, got: %#v", stateNamespace)
	}

	stateKey := configEntryByKey(entries, "rke2_patcher_state_key")
	if stateKey.Key != "" {
		t.Fatalf("expected rke2_patcher_state_key to be hidden, got: %#v", stateKey)
	}

	helmNamespace := configEntryByKey(entries, "helm_namespace")
	if helmNamespace.Key != "" {
		t.Fatalf("expected helm_namespace to be hidden, got: %#v", helmNamespace)
	}
}

func TestCollectConfigEntries_InvalidValues(t *testing.T) {
	t.Setenv(cveModeEnvName, "invalid")
	if _, err := collectConfigEntries(); err == nil {
		t.Fatalf("expected scanner mode validation error, got nil")
	}

	t.Setenv(cveModeEnvName, "cluster")
	t.Setenv(cveJobTimeoutEnvName, "0")
	if _, err := collectConfigEntries(); err == nil {
		t.Fatalf("expected timeout validation error, got nil")
	}
}

func configEntryByKey(entries []configEntry, key string) configEntry {
	for _, entry := range entries {
		if entry.Key == key {
			return entry
		}
	}
	return configEntry{}
}
