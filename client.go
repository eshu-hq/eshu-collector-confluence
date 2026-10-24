// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eshu-hq/eshu-collector-confluence/internal/httpx"
)

const (
	defaultHTTPTimeout = 30 * time.Second
	// maxCursorPages defensively bounds the paginated fetches issued while
	// walking one cursor (_links.next) chain, even when max_total_pages is set
	// very high. It guards against a provider that never ends the chain.
	maxCursorPages = 200
	// maxRedirects bounds same-host redirects the default client follows.
	maxRedirects = 3
	// maxRetryAfter bounds provider retry guidance so one header cannot park a
	// claim for hours.
	maxRetryAfter = time.Hour
)

// Client reads bounded Confluence source evidence with GET requests only.
// ListSpacePages and ListPageTree take a per-request limit and a total-record
// cap across the whole cursor walk and report whether that cap (or a defensive
// stop) dropped records the provider still had to return.
type Client interface {
	GetSpace(ctx context.Context, id string) (Space, error)
	ListSpacePages(ctx context.Context, spaceID string, limit int, maxTotalPages int) (pages []Page, truncated bool, err error)
	ListPageTree(ctx context.Context, rootPageID string, limit int, maxTotalPages int) (ids []string, truncated bool, err error)
	GetPage(ctx context.Context, id string) (Page, error)
}

// HTTPClientConfig configures the read-only Confluence HTTP client.
type HTTPClientConfig struct {
	BaseURL          string
	Credentials      Credentials
	HTTP             *http.Client
	MaxResponseBytes int64
	// Clock supplies the time used to interpret an HTTP-date Retry-After.
	Clock func() time.Time
}

// HTTPClient is a read-only Confluence Cloud REST API v2 client. It issues GET
// requests only and never sends credentials to a host other than BaseURL.
type HTTPClient struct {
	baseURL     *url.URL
	credentials Credentials
	client      *http.Client
	maxBytes    int64
	clock       func() time.Time
	stats       *requestStats
}

// NewHTTPClient creates a read-only Confluence HTTP client.
func NewHTTPClient(config HTTPClientConfig) (*HTTPClient, error) {
	baseURL, err := httpx.ParseBaseURL("confluence", config.BaseURL)
	if err != nil {
		return nil, err
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, errors.New("confluence base_url must use http or https")
	}
	creds := config.Credentials
	if strings.TrimSpace(creds.BearerToken) == "" &&
		(strings.TrimSpace(creds.Email) == "" || strings.TrimSpace(creds.APIToken) == "") {
		return nil, errors.New("read-only Confluence API credentials are required")
	}
	client := config.HTTP
	if client == nil {
		client = httpx.DefaultClient(defaultHTTPTimeout)
		client.CheckRedirect = sameHostRedirects(baseURL)
	}
	maxBytes := config.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxResponseBytes
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	return &HTTPClient{
		baseURL:     baseURL,
		credentials: creds,
		client:      client,
		maxBytes:    maxBytes,
		clock:       clock,
		stats:       &requestStats{},
	}, nil
}

// sameHostRedirects refuses a redirect that leaves the configured host, so
// the Authorization header never follows a response to another origin.
func sameHostRedirects(base *url.URL) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return http.ErrUseLastResponse
		}
		if !sameOrigin(req.URL, base) {
			return http.ErrUseLastResponse
		}
		return nil
	}
}

func sameOrigin(candidate, base *url.URL) bool {
	return strings.EqualFold(candidate.Scheme, base.Scheme) && strings.EqualFold(candidate.Host, base.Host)
}

// GetSpace reads one Confluence space.
func (c *HTTPClient) GetSpace(ctx context.Context, id string) (Space, error) {
	var space Space
	if err := c.getJSON(ctx, "/api/v2/spaces/"+url.PathEscape(id), nil, &space); err != nil {
		return Space{}, err
	}
	return space, nil
}

// ListSpacePages reads current pages visible in a Confluence space, bounded by
// both the per-request limit and maxTotalPages across the whole cursor walk.
// The returned bool is true when the walk stopped (at maxTotalPages, the
// defensive maxCursorPages backstop, or a repeated cursor) while the provider
// still had more data to return; it is never a false positive from the walk's
// own natural end.
func (c *HTTPClient) ListSpacePages(ctx context.Context, spaceID string, limit int, maxTotalPages int) ([]Page, bool, error) {
	values := url.Values{}
	values.Set("body-format", "storage")
	values.Set("status", "current")
	values.Set("limit", strconv.Itoa(limit))
	maxTotalPages = normalizedMaxTotalPages(maxTotalPages)

	var out []Page
	seen := map[string]struct{}{}
	endpoint := "/api/v2/spaces/" + url.PathEscape(spaceID) + "/pages"
	fetched := 0
	for endpoint != "" {
		if fetched >= maxCursorPages {
			return out, true, nil
		}
		if _, repeated := seen[endpoint]; repeated {
			return out, true, nil
		}
		seen[endpoint] = struct{}{}
		var response pageListResponse
		if err := c.getJSON(ctx, endpoint, values, &response); err != nil {
			return nil, false, err
		}
		fetched++
		out = append(out, response.Results...)
		endpoint = response.Links.Next
		values = nil
		if len(out) >= maxTotalPages {
			// Compute truncation before slicing: records are dropped when this
			// page overflowed the cap even if it was the terminal page.
			truncated := len(out) > maxTotalPages || endpoint != ""
			return out[:maxTotalPages], truncated, nil
		}
	}
	return out, false, nil
}

// ListPageTree returns the root page id and descendant page ids, bounded by
// both the per-request limit and maxTotalPages (including the root id itself)
// across the whole cursor walk. See ListSpacePages for the truncated contract.
func (c *HTTPClient) ListPageTree(ctx context.Context, rootPageID string, limit int, maxTotalPages int) ([]string, bool, error) {
	values := url.Values{}
	values.Set("limit", strconv.Itoa(limit))
	maxTotalPages = normalizedMaxTotalPages(maxTotalPages)

	ids := []string{rootPageID}
	seen := map[string]struct{}{}
	endpoint := "/api/v2/pages/" + url.PathEscape(rootPageID) + "/descendants"
	fetched := 0
	for endpoint != "" {
		if fetched >= maxCursorPages {
			return ids, true, nil
		}
		if _, repeated := seen[endpoint]; repeated {
			return ids, true, nil
		}
		seen[endpoint] = struct{}{}
		var response struct {
			Results []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"results"`
			Links Links `json:"_links"`
		}
		if err := c.getJSON(ctx, endpoint, values, &response); err != nil {
			return nil, false, err
		}
		fetched++
		for _, result := range response.Results {
			if strings.EqualFold(result.Type, "page") && strings.TrimSpace(result.ID) != "" {
				ids = append(ids, result.ID)
			}
		}
		endpoint = response.Links.Next
		values = nil
		if len(ids) >= maxTotalPages {
			truncated := len(ids) > maxTotalPages || endpoint != ""
			return ids[:maxTotalPages], truncated, nil
		}
	}
	return ids, false, nil
}

// normalizedMaxTotalPages defends the list walks against a caller that left
// the cap at zero.
func normalizedMaxTotalPages(value int) int {
	if value <= 0 {
		return defaultMaxTotalPages
	}
	return value
}

// GetPage reads one Confluence page with body, labels, and version.
func (c *HTTPClient) GetPage(ctx context.Context, id string) (Page, error) {
	values := url.Values{}
	values.Set("body-format", "storage")
	values.Set("include-labels", "true")
	values.Set("include-version", "true")
	var page Page
	if err := c.getJSON(ctx, "/api/v2/pages/"+url.PathEscape(id), values, &page); err != nil {
		return Page{}, err
	}
	return page, nil
}

func (c *HTTPClient) getJSON(ctx context.Context, endpoint string, query url.Values, target any) error {
	requestURL, err := c.resolve(endpoint)
	if err != nil {
		c.stats.record(operationOf(endpoint), "untrusted_link")
		return err
	}
	operation := operationOf(requestURL.Path)
	if query != nil {
		requestURL.RawQuery = query.Encode()
	}
	outcome := "transport_error"
	defer func() { c.stats.record(operation, outcome) }()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		outcome = "request_build_error"
		return providerError{class: FailureInternal}
	}
	req.Header.Set("Accept", "application/json")
	if c.credentials.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.credentials.BearerToken)
	} else {
		req.SetBasicAuth(c.credentials.Email, c.credentials.APIToken)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return c.transportError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if handled, failure := c.statusFailure(resp, &outcome); handled {
		return failure
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		outcome = "read_error"
		return c.transportError(ctx, err)
	}
	if int64(len(body)) > c.maxBytes {
		outcome = "response_too_large"
		return providerError{class: FailureResponseTooLarge}
	}
	if err := json.Unmarshal(body, target); err != nil {
		outcome = "decode_error"
		return providerError{class: FailureInvalidResponse}
	}
	outcome = "success"
	return nil
}

// transportError classifies a connection-level failure. A transient one is
// retryable; anything else (TLS, DNS not found, malformed response) is terminal.
func (c *HTTPClient) transportError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if httpx.IsTransientTransportError(ctx, err) {
		return providerError{class: FailureTransport, retryable: true, cause: err}
	}
	return providerError{class: FailureConnectionFailed, cause: err}
}

// statusFailure maps a non-2xx response to a bounded error. It reports false
// for a success status. The response body is never read or echoed.
func (c *HTTPClient) statusFailure(resp *http.Response, outcome *string) (bool, error) {
	status := resp.StatusCode
	*outcome = statusClass(status)
	switch {
	case status >= 200 && status < 300:
		return false, nil
	case status == http.StatusForbidden || status == http.StatusNotFound:
		*outcome = "permission_denied"
		return true, ErrPermissionDenied
	case status == http.StatusUnauthorized:
		return true, providerError{class: FailureAuthFailed, statusCode: status}
	case status == http.StatusTooManyRequests:
		*outcome = "rate_limited"
		return true, providerError{class: FailureRateLimited, retryable: true, statusCode: status, retryAfter: c.retryAfter(resp)}
	case isRetryableServerStatus(status):
		*outcome = "retryable_status"
		return true, providerError{class: FailureProviderUnavailable, retryable: true, statusCode: status, retryAfter: c.retryAfter(resp)}
	default:
		return true, providerError{class: FailureProviderRejected, statusCode: status}
	}
}

func isRetryableServerStatus(status int) bool {
	switch status {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func (c *HTTPClient) retryAfter(resp *http.Response) time.Duration {
	delay := httpx.ParseRetryAfter(resp.Header.Get("Retry-After"), c.clock())
	if delay > maxRetryAfter {
		return maxRetryAfter
	}
	return delay
}

// resolve turns a relative or absolute endpoint into a request URL. An
// absolute endpoint (a pagination link) is accepted only on the configured
// origin, so credentials never follow a link to another host.
func (c *HTTPClient) resolve(endpoint string) (url.URL, error) {
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		parsed, err := url.Parse(endpoint)
		if err != nil || !sameOrigin(parsed, c.baseURL) {
			return url.URL{}, providerError{class: FailureUntrustedLink}
		}
		return *parsed, nil
	}
	resolved := *c.baseURL
	relative, err := url.Parse(endpoint)
	if err != nil {
		resolved.Path = c.resolvePath(endpoint)
		return resolved, nil
	}
	if relative.Host != "" {
		// A scheme-relative link (//host/path) names another origin.
		return url.URL{}, providerError{class: FailureUntrustedLink}
	}
	resolved.Path = c.resolvePath(relative.Path)
	resolved.RawQuery = relative.RawQuery
	resolved.Fragment = relative.Fragment
	return resolved, nil
}

func (c *HTTPClient) resolvePath(endpointPath string) string {
	if endpointPath == "" {
		return c.baseURL.Path
	}
	basePath := cleanURLPath(c.baseURL.Path)
	resolvedPath := cleanURLPath(endpointPath)
	if basePath == "/" {
		return resolvedPath
	}
	if resolvedPath == basePath || strings.HasPrefix(resolvedPath, basePath+"/") {
		return resolvedPath
	}
	return path.Join(basePath, resolvedPath)
}

func cleanURLPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	return path.Clean("/" + strings.TrimPrefix(value, "/"))
}

func operationOf(requestPath string) string {
	cleaned := cleanURLPath(requestPath)
	switch {
	case strings.HasSuffix(cleaned, "/descendants"):
		return "list_page_tree"
	case strings.HasSuffix(cleaned, "/pages"):
		return "list_pages"
	case strings.Contains(cleaned, "/pages/"):
		return "fetch_page"
	case strings.Contains(cleaned, "/spaces/"):
		return "get_space"
	default:
		return "unknown"
	}
}

func statusClass(status int) string {
	if status < 100 {
		return "unknown"
	}
	return strconv.Itoa(status/100) + "xx"
}

// requestStats counts provider requests by bounded operation and outcome. It
// feeds the per-claim stderr debug line; it carries no URL or id.
// Workers call record concurrently, so it is mutex-guarded.
type requestStats struct {
	mu     sync.Mutex
	counts map[string]int64
}

func (s *requestStats) record(operation, outcome string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts == nil {
		s.counts = map[string]int64{}
	}
	s.counts[operation+"/"+outcome]++
}

// snapshot returns a copy of the request counts by operation/outcome.
func (s *requestStats) snapshot() map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int64, len(s.counts))
	for key, value := range s.counts {
		out[key] = value
	}
	return out
}
