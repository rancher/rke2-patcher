package registry

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultRegistryHost = "registry.rancher.com"
	registryEnv         = "RKE2_PATCHER_REGISTRY"
	registryCAFileEnv   = "RKE2_PATCHER_REGISTRY_CA_FILE"
	defaultPage         = 100
)

type Tag struct {
	Name        string
	LastUpdated time.Time
}

type tagsPage struct {
	Tags []string `json:"tags"`
}

// realm: is the URL of the auth server
// service: is the name of the registry requesting the token
// scope: what permission we are asking (e.g. repository:rancher/hardened-traefik:pull)
type bearerChallenge struct {
	Realm   string
	Service string
	Scope   string
}

// ListTags retrieves a list of tags for the specified repository from the registry, handling pagination
// and authentication as needed. The limit parameter controls how many tags to retrieve, and must be greater
// than zero.
func ListTags(repository string, limit int) ([]Tag, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be greater than zero")
	}

	baseURL, repositoryPath, err := resolveImageRepo(repository)
	if err != nil {
		return nil, err
	}

	pageSize := defaultPage
	if limit < pageSize {
		pageSize = limit
	}

	next := fmt.Sprintf("%s/v2/%s/tags/list?n=%d", baseURL, escapeRepositoryPath(repositoryPath), pageSize)
	client, err := newRegistryHTTPClient()
	if err != nil {
		return nil, err
	}
	tags := make([]Tag, 0, limit)
	seen := make(map[string]struct{}, limit)
	bearerToken := ""

	for next != "" && len(tags) < limit {
		page, nextURL, resolvedToken, pageErr := getTagsPage(client, next, baseURL, repositoryPath, bearerToken)
		if pageErr != nil {
			return nil, pageErr
		}
		if resolvedToken != "" {
			bearerToken = resolvedToken
		}

		for _, name := range page.Tags {
			// There are some weird tags that we want to filter out
			if strings.EqualFold(name, "latest") {
				continue
			}

			lowerName := strings.ToLower(name)
			if strings.HasPrefix(lowerName, "sha256-") {
				continue
			}
			if strings.HasSuffix(lowerName, ".sig") || strings.HasSuffix(lowerName, ".att") {
				continue
			}

			if _, found := seen[name]; found {
				continue
			}

			seen[name] = struct{}{}
			tags = append(tags, Tag{Name: name})

			if len(tags) == limit {
				break
			}
		}

		next = nextURL
	}

	if len(tags) == 0 {
		return nil, fmt.Errorf("no tags found for repository %q", repository)
	}

	return tags, nil
}

func newRegistryHTTPClient() (*http.Client, error) {
	caFile := strings.TrimSpace(os.Getenv(registryCAFileEnv))
	if caFile == "" {
		return &http.Client{Timeout: 20 * time.Second}, nil
	}

	pemBytes, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s %q: %w", registryCAFileEnv, caFile, err)
	}

	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("failed to parse PEM certificates from %s %q", registryCAFileEnv, caFile)
	}

	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("expected http.DefaultTransport to be *http.Transport, got %T", http.DefaultTransport)
	}

	transport := defaultTransport.Clone()
	if transport.TLSClientConfig != nil {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	} else {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.RootCAs = pool

	return &http.Client{Timeout: 20 * time.Second, Transport: transport}, nil
}

func LatestTag(repository string) (Tag, error) {
	tags, err := ListTags(repository, 1)
	if err != nil {
		return Tag{}, err
	}

	return tags[0], nil
}

// getTagsPage retrieves a single page of tags from the registry API, handling bearer token authentication if necessary (needed for suse registry)
func getTagsPage(client *http.Client, requestURL string, baseURL string, repository string, bearerToken string) (tagsPage, string, string, error) {

	// First attempt with whatever bearer token we have (empty)
	page, nextURL, err := getTagsPageWithBearer(client, requestURL, baseURL, bearerToken)
	if err == nil {
		return page, nextURL, bearerToken, nil
	}

	// if error is 401 (StatusUnauthorized) we try again because it might be that the registry requires a
	// bearer token and we didn't have one or had an invalid one, so we need to fetch a new token and retry
	// the request. This is apparently typical for OCI registries like SUSE's
	statusErr, ok := err.(httpStatusError)
	if !ok || statusErr.StatusCode != http.StatusUnauthorized {
		return tagsPage{}, "", "", err
	}

	// The 401 error might contain the information we need to authenticate
	challenge, parseErr := parseBearerChallenge(statusErr.WWWAuthenticate)
	if parseErr != nil {
		return tagsPage{}, "", "", parseErr
	}

	if strings.TrimSpace(challenge.Scope) == "" {
		challenge.Scope = fmt.Sprintf("repository:%s:pull", repository)
	}

	// Now we request a temporary bearer token with the information from the challenge
	token, tokenErr := fetchBearerToken(client, challenge)
	if tokenErr != nil {
		return tagsPage{}, "", "", tokenErr
	}

	// We try again with the new token
	page, nextURL, err = getTagsPageWithBearer(client, requestURL, baseURL, token)
	if err != nil {
		return tagsPage{}, "", "", err
	}

	return page, nextURL, token, nil
}

// getTagsPageWithBearer performs the actual HTTP request to get a page of tags, using the provided bearer token for authentication
func getTagsPageWithBearer(client *http.Client, requestURL string, baseURL string, bearerToken string) (tagsPage, string, error) {
	request, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return tagsPage{}, "", err
	}

	bearerToken = strings.TrimSpace(bearerToken)
	if bearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	response, err := client.Do(request)
	if err != nil {
		return tagsPage{}, "", err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return tagsPage{}, "", httpStatusError{
			StatusCode:      response.StatusCode,
			Body:            strings.TrimSpace(string(bodyBytes)),
			WWWAuthenticate: strings.TrimSpace(response.Header.Get("Www-Authenticate")),
		}
	}

	var page tagsPage
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		return tagsPage{}, "", err
	}

	nextURL, err := parseNextPageURL(response.Header.Get("Link"), baseURL)
	if err != nil {
		return tagsPage{}, "", err
	}

	return page, nextURL, nil
}

// fetchBearerToken requests a bearer token from the registry's auth server using the information provided
// in the WWW-Authenticate challenge
func fetchBearerToken(client *http.Client, challenge bearerChallenge) (string, error) {
	authURL, err := url.Parse(challenge.Realm)
	if err != nil {
		return "", fmt.Errorf("invalid registry authorization realm %q: %w", challenge.Realm, err)
	}

	query := authURL.Query()
	if challenge.Service != "" {
		query.Set("service", challenge.Service)
	}
	if challenge.Scope != "" {
		query.Set("scope", challenge.Scope)
	}
	authURL.RawQuery = query.Encode()

	response, err := client.Get(authURL.String())
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return "", fmt.Errorf("registry token endpoint returned status %d: %s", response.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}

	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", err
	}

	token := strings.TrimSpace(payload.Token)
	if token == "" {
		token = strings.TrimSpace(payload.AccessToken)
	}

	if token == "" {
		return "", fmt.Errorf("registry token endpoint did not return a token")
	}

	return token, nil
}

// parseBearerChallenge parses the WWW-Authenticate header value from a 401 Unauthorized response to extract
// the information needed to request a bearer token for registry authentication
func parseBearerChallenge(headerValue string) (bearerChallenge, error) {
	trimmed := strings.TrimSpace(headerValue)
	if trimmed == "" {
		return bearerChallenge{}, fmt.Errorf("registry returned unauthorized without WWW-Authenticate header")
	}

	prefix := "bearer "
	if !strings.HasPrefix(strings.ToLower(trimmed), prefix) {
		return bearerChallenge{}, fmt.Errorf("unsupported registry auth challenge %q", headerValue)
	}

	params := strings.TrimSpace(trimmed[len(prefix):])
	parts := strings.Split(params, ",")

	challenge := bearerChallenge{}
	for _, part := range parts {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}

		key = strings.ToLower(strings.TrimSpace(key))
		decoded := strings.Trim(strings.TrimSpace(value), "\"")
		switch key {
		case "realm":
			challenge.Realm = decoded
		case "service":
			challenge.Service = decoded
		case "scope":
			challenge.Scope = decoded
		}
	}

	if challenge.Realm == "" {
		return bearerChallenge{}, fmt.Errorf("registry authorization challenge did not include a realm")
	}

	return challenge, nil
}

// parseNextPageURL parses the Link header from the registry API response to extract the URL for the next page
// of tags, if present
func parseNextPageURL(linkHeader string, baseURL string) (string, error) {
	if strings.TrimSpace(linkHeader) == "" {
		return "", nil
	}

	start := strings.Index(linkHeader, "<")
	end := strings.Index(linkHeader, ">")
	if start < 0 || end <= start+1 {
		return "", fmt.Errorf("invalid Link header for tags pagination: %q", linkHeader)
	}

	next := strings.TrimSpace(linkHeader[start+1 : end])
	if next == "" {
		return "", nil
	}

	parsedNext, err := url.Parse(next)
	if err != nil {
		return "", fmt.Errorf("invalid next tags URL %q: %w", next, err)
	}

	if parsedNext.IsAbs() {
		return parsedNext.String(), nil
	}

	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}

	return base.ResolveReference(parsedNext).String(), nil
}

func resolveImageRepo(repository string) (string, string, error) {
	repositoryPath, err := normalizeRepositoryPath(repository)
	if err != nil {
		return "", "", err
	}

	rawValue := strings.TrimSpace(os.Getenv(registryEnv))
	if rawValue == "" {
		rawValue = defaultRegistryHost
	}

	if !strings.Contains(rawValue, "://") {
		rawValue = "https://" + rawValue
	}

	parsed, err := url.Parse(rawValue)
	if err != nil {
		return "", "", fmt.Errorf("invalid %s value %q: %w", registryEnv, rawValue, err)
	}

	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "https" && scheme != "http" {
		return "", "", fmt.Errorf("invalid %s value %q: scheme must be http or https", registryEnv, rawValue)
	}

	host := strings.TrimSpace(parsed.Host)
	if host == "" {
		return "", "", fmt.Errorf("invalid %s value %q: missing registry host", registryEnv, rawValue)
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed.String(), repositoryPath, nil
}

func normalizeRepositoryPath(repository string) (string, error) {
	trimmed := strings.Trim(strings.TrimSpace(repository), "/")
	if trimmed == "" {
		return "", fmt.Errorf("repository %q must be in the format <namespace>/<repo>", repository)
	}

	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("repository %q must be in the format <namespace>/<repo>", repository)
	}

	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return "", fmt.Errorf("repository %q must be in the format <namespace>/<repo>", repository)
		}
	}

	return trimmed, nil
}

// escapeRepositoryPath applies URL path escaping to each segment of the repository path to ensure special characters are properly encoded in the API request URL
func escapeRepositoryPath(repository string) string {
	parts := strings.Split(repository, "/")
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		escaped = append(escaped, url.PathEscape(part))
	}

	return strings.Join(escaped, "/")
}

type httpStatusError struct {
	StatusCode      int
	Body            string
	WWWAuthenticate string
}

func (err httpStatusError) Error() string {
	if strings.TrimSpace(err.Body) == "" {
		return fmt.Sprintf("registry API returned status %d", err.StatusCode)
	}

	return fmt.Sprintf("registry API returned status %d: %s", err.StatusCode, err.Body)
}
