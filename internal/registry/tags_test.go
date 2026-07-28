package registry

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBearerChallenge(t *testing.T) {
	t.Run("valid challenge", func(t *testing.T) {
		header := `Bearer realm="https://scc.suse.com/api/registry/authorize",service="SUSE Linux Docker Registry",scope="repository:rancher/hardened-traefik:pull"`
		challenge, err := parseBearerChallenge(header)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if challenge.Realm != "https://scc.suse.com/api/registry/authorize" {
			t.Fatalf("unexpected realm: %q", challenge.Realm)
		}

		if challenge.Service != "SUSE Linux Docker Registry" {
			t.Fatalf("unexpected service: %q", challenge.Service)
		}

		if challenge.Scope != "repository:rancher/hardened-traefik:pull" {
			t.Fatalf("unexpected scope: %q", challenge.Scope)
		}
	})

	t.Run("missing realm", func(t *testing.T) {
		_, err := parseBearerChallenge(`Bearer service="svc"`)
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})
}

func TestParseNextPageURL(t *testing.T) {
	t.Run("absolute next link", func(t *testing.T) {
		next, err := parseNextPageURL(`<https://registry.rancher.com/v2/rancher/hardened-traefik/tags/list?n=100&last=abc>; rel="next"`, "https://registry.rancher.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expected := "https://registry.rancher.com/v2/rancher/hardened-traefik/tags/list?n=100&last=abc"
		if next != expected {
			t.Fatalf("unexpected next URL: %q", next)
		}
	})

	t.Run("relative next link", func(t *testing.T) {
		next, err := parseNextPageURL(`</v2/rancher/hardened-traefik/tags/list?n=100&last=abc>; rel="next"`, "https://registry.rancher.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expected := "https://registry.rancher.com/v2/rancher/hardened-traefik/tags/list?n=100&last=abc"
		if next != expected {
			t.Fatalf("unexpected next URL: %q", next)
		}
	})

	t.Run("empty header", func(t *testing.T) {
		next, err := parseNextPageURL("", "https://registry.rancher.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if next != "" {
			t.Fatalf("expected empty next URL, got %q", next)
		}
	})
}

func TestResolveListTagsInputs(t *testing.T) {
	t.Run("default host", func(t *testing.T) {
		t.Setenv(registryEnv, "")
		baseURL, repository, err := resolveImageRepo("rancher/hardened-traefik")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if baseURL != "https://"+defaultRegistryHost {
			t.Fatalf("unexpected base URL: %q", baseURL)
		}

		if repository != "rancher/hardened-traefik" {
			t.Fatalf("unexpected repository: %q", repository)
		}
	})

	t.Run("host without scheme", func(t *testing.T) {
		t.Setenv(registryEnv, "mirror.local:5000")
		baseURL, _, err := resolveImageRepo("rancher/hardened-traefik")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if baseURL != "https://mirror.local:5000" {
			t.Fatalf("unexpected base URL: %q", baseURL)
		}
	})

	t.Run("value with scheme and path", func(t *testing.T) {
		t.Setenv(registryEnv, "http://registry.local/proxy/")
		baseURL, _, err := resolveImageRepo("rancher/hardened-traefik")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if baseURL != "http://registry.local/proxy" {
			t.Fatalf("unexpected base URL: %q", baseURL)
		}
	})

	t.Run("invalid scheme", func(t *testing.T) {
		t.Setenv(registryEnv, "ftp://registry.local")
		_, _, err := resolveImageRepo("rancher/hardened-traefik")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})

	t.Run("missing host", func(t *testing.T) {
		t.Setenv(registryEnv, "https://")
		_, _, err := resolveImageRepo("rancher/hardened-traefik")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})

	t.Run("invalid repository", func(t *testing.T) {
		t.Setenv(registryEnv, "")
		_, _, err := resolveImageRepo("hardened-traefik")
		if err != nil {
			return
		}
		t.Fatalf("expected error, got nil")
	})
}

func TestFetchBearerToken_BasicAuth(t *testing.T) {
	t.Run("sends basic auth when credentials are set", func(t *testing.T) {
		t.Setenv("RKE2_PATCHER_REGISTRY_USERNAME", "someuser")
		t.Setenv("RKE2_PATCHER_REGISTRY_PASSWORD", "sometoken")

		var gotUser, gotPass string
		var gotOK bool

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUser, gotPass, gotOK = r.BasicAuth()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "authed-token"})
		}))
		defer server.Close()

		token, err := fetchBearerToken(&http.Client{}, bearerChallenge{Realm: server.URL})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if token != "authed-token" {
			t.Fatalf("unexpected token: %q", token)
		}
		if !gotOK {
			t.Fatalf("expected request to include basic auth credentials")
		}
		if gotUser != "someuser" || gotPass != "sometoken" {
			t.Fatalf("unexpected basic auth credentials: user=%q pass=%q", gotUser, gotPass)
		}
	})

	t.Run("omits basic auth when credentials are unset", func(t *testing.T) {
		t.Setenv("RKE2_PATCHER_REGISTRY_USERNAME", "")
		t.Setenv("RKE2_PATCHER_REGISTRY_PASSWORD", "")

		var gotOK bool

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _, gotOK = r.BasicAuth()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "anon-token"})
		}))
		defer server.Close()

		if _, err := fetchBearerToken(&http.Client{}, bearerChallenge{Realm: server.URL}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotOK {
			t.Fatalf("expected request to omit basic auth credentials")
		}
	})

	t.Run("omits basic auth when only username is set", func(t *testing.T) {
		t.Setenv("RKE2_PATCHER_REGISTRY_USERNAME", "someuser")
		t.Setenv("RKE2_PATCHER_REGISTRY_PASSWORD", "")

		var gotOK bool

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _, gotOK = r.BasicAuth()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "anon-token"})
		}))
		defer server.Close()

		if _, err := fetchBearerToken(&http.Client{}, bearerChallenge{Realm: server.URL}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotOK {
			t.Fatalf("expected request to omit basic auth credentials when password is missing")
		}
	})

	t.Run("refuses to send credentials to non-https token endpoints", func(t *testing.T) {
		t.Setenv("RKE2_PATCHER_REGISTRY_USERNAME", "someuser")
		t.Setenv("RKE2_PATCHER_REGISTRY_PASSWORD", "sometoken")

		_, err := fetchBearerToken(&http.Client{}, bearerChallenge{Realm: "http://example.com/token"})
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "refusing to send registry credentials") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestListTags_WithRegistryCAFile(t *testing.T) {
	repository := "rancher/hardened-traefik"
	server := newTLSTagsServer(t, repository, []string{"v1.0.0-build20260101"})
	t.Setenv(registryEnv, server.URL)

	_, err := ListTags(repository, 1)
	if err == nil {
		t.Fatalf("expected TLS validation error without CA file, got nil")
	}

	caFile := writeServerCertAsCAFile(t, server)
	t.Setenv(registryCAFileEnv, caFile)

	tags, err := ListTags(repository, 1)
	if err != nil {
		t.Fatalf("unexpected error with custom CA file: %v", err)
	}
	if len(tags) != 1 || tags[0].Name != "v1.0.0-build20260101" {
		t.Fatalf("unexpected tags: %#v", tags)
	}
}

func TestNewRegistryHTTPClient_InvalidCAFile(t *testing.T) {
	invalidPath := filepath.Join(t.TempDir(), "missing.pem")
	t.Setenv(registryCAFileEnv, invalidPath)

	_, err := newRegistryHTTPClient()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), registryCAFileEnv) {
		t.Fatalf("expected error mentioning %s, got %v", registryCAFileEnv, err)
	}
}

func newTLSTagsServer(t *testing.T, repository string, tags []string) *httptest.Server {
	t.Helper()

	path := fmt.Sprintf("/v2/%s/tags/list", repository)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"tags":["%s"]}`, strings.Join(tags, `","`))
	})

	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server
}

func writeServerCertAsCAFile(t *testing.T, server *httptest.Server) string {
	t.Helper()

	certDER := server.TLS.Certificates[0].Certificate[0]
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("failed to parse test server certificate: %v", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	caFile := filepath.Join(t.TempDir(), "registry-ca.pem")
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatalf("failed to write CA file: %v", err)
	}

	return caFile
}

func TestGetTagsPageWithBearer_RefusesTokenOverHTTP(t *testing.T) {
	t.Run("refuses bearer token on non-https endpoint", func(t *testing.T) {
		_, _, err := getTagsPageWithBearer(&http.Client{}, "http://registry.local/v2/rancher/hardened-traefik/tags/list?n=1", "http://registry.local", "some-token")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "refusing to send bearer token") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("allows bearer token on loopback", func(t *testing.T) {
		var gotAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tags":["v1.0.0"]}`))
		}))
		defer server.Close()

		_, _, err := getTagsPageWithBearer(&http.Client{}, server.URL+"/v2/rancher/hardened-traefik/tags/list?n=1", server.URL, "loopback-token")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotAuth != "Bearer loopback-token" {
			t.Fatalf("expected bearer token to be sent to loopback, got %q", gotAuth)
		}
	})
}