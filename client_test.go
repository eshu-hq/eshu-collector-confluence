// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func newTestClient(t *testing.T, site *fakeSite, creds Credentials) *HTTPClient {
	t.Helper()
	client, err := NewHTTPClient(HTTPClientConfig{BaseURL: site.baseURL(), Credentials: creds})
	if err != nil {
		t.Fatalf("NewHTTPClient() error = %v", err)
	}
	return client
}

func TestHTTPClientUsesGETWithBasicOrBearerAuth(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	basic := newTestClient(t, site, Credentials{Email: "bot@example.invalid", APIToken: canaryToken})
	if _, err := basic.GetSpace(t.Context(), "12345"); err != nil {
		t.Fatalf("GetSpace() error = %v", err)
	}
	bearer := newTestClient(t, site, Credentials{BearerToken: canaryToken, Email: "ignored@example.invalid", APIToken: "ignored"})
	if _, err := bearer.GetPage(t.Context(), "1001"); err != nil {
		t.Fatalf("GetPage() error = %v", err)
	}
	requests := site.recorded()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	for _, request := range requests {
		if request.method != http.MethodGet {
			t.Errorf("method = %s, want GET", request.method)
		}
	}
	if !strings.HasPrefix(requests[0].auth, "Basic ") {
		t.Errorf("first auth = %q, want basic", requests[0].auth)
	}
	if requests[1].auth != "Bearer "+canaryToken {
		t.Errorf("second auth = %q, want bearer winning over basic", requests[1].auth)
	}
}

func TestHTTPClientRefusesCredentiallessOrBadBaseURL(t *testing.T) {
	t.Parallel()

	tests := map[string]HTTPClientConfig{
		"no credentials":   {BaseURL: "https://confluence.example.invalid/wiki"},
		"email only":       {BaseURL: "https://confluence.example.invalid/wiki", Credentials: Credentials{Email: "a@example.invalid"}},
		"userinfo in base": {BaseURL: "https://u:p@confluence.example.invalid/wiki", Credentials: Credentials{BearerToken: "x"}},
		"ftp scheme":       {BaseURL: "ftp://confluence.example.invalid/wiki", Credentials: Credentials{BearerToken: "x"}},
		"no host":          {BaseURL: "https://", Credentials: Credentials{BearerToken: "x"}},
	}
	for name, config := range tests {
		if _, err := NewHTTPClient(config); err == nil {
			t.Errorf("%s: NewHTTPClient() error = nil, want a refusal", name)
		}
	}
}

// otherHost records every request that reaches a host the client must not
// talk to, so a test can prove credentials never left the base origin.
type otherHost struct {
	mu   sync.Mutex
	hits []string
	auth []string
	*httptest.Server
}

func newOtherHost(t *testing.T) *otherHost {
	t.Helper()
	other := &otherHost{}
	other.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other.mu.Lock()
		other.hits = append(other.hits, r.URL.Path)
		other.auth = append(other.auth, r.Header.Get("Authorization"))
		other.mu.Unlock()
		writeJSON(w, map[string]any{"results": []any{}})
	}))
	t.Cleanup(other.Close)
	return other
}

func TestHTTPClientNeverFollowsAPaginationLinkToAnotherOrigin(t *testing.T) {
	t.Parallel()

	for name, link := range map[string]func(other *otherHost) string{
		"absolute other host": func(o *otherHost) string { return o.URL + "/steal" },
		"scheme relative":     func(o *otherHost) string { return "//" + strings.TrimPrefix(o.URL, "http://") + "/steal" },
		"other scheme":        func(o *otherHost) string { return strings.Replace(o.URL, "http://", "https://", 1) + "/steal" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			site := twoPageSite(t)
			other := newOtherHost(t)
			site.mu.Lock()
			site.override["/wiki/api/v2/spaces/12345/pages"] = func(w http.ResponseWriter, r *http.Request) bool {
				writeJSON(w, map[string]any{
					"results": []Page{samplePage("1001", "A")},
					"_links":  map[string]string{"next": link(other)},
				})
				return true
			}
			site.mu.Unlock()
			client := newTestClient(t, site, Credentials{BearerToken: canaryToken})
			_, _, err := client.ListSpacePages(t.Context(), "12345", 10, 100)
			var provider providerError
			if !errors.As(err, &provider) || provider.class != FailureUntrustedLink {
				t.Fatalf("ListSpacePages() error = %v, want untrusted-next-link", err)
			}
			other.mu.Lock()
			defer other.mu.Unlock()
			if len(other.hits) != 0 {
				t.Fatalf("credentials were sent to another origin: %v %v", other.hits, other.auth)
			}
		})
	}
}

func TestHTTPClientFollowsSameOriginAbsoluteLinks(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	for _, id := range []string{"9001", "9002", "9003"} {
		site.addPage(samplePage(id, id))
	}
	site.mu.Lock()
	site.override["/wiki/api/v2/spaces/12345/pages"] = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Query().Get("cursor") != "" {
			return false
		}
		writeJSON(w, map[string]any{
			"results": []Page{site.pages["9001"]},
			"_links":  map[string]string{"next": site.server.URL + "/wiki/api/v2/spaces/12345/pages?cursor=1&limit=10"},
		})
		return true
	}
	site.mu.Unlock()
	client := newTestClient(t, site, Credentials{BearerToken: canaryToken})
	pages, truncated, err := client.ListSpacePages(t.Context(), "12345", 10, 100)
	if err != nil || truncated || len(pages) != 3 {
		t.Fatalf("ListSpacePages() = %d pages truncated=%v err=%v, want 3 pages walked across the absolute link", len(pages), truncated, err)
	}
}

func TestHTTPClientDoesNotFollowRedirectsToAnotherHost(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	other := newOtherHost(t)
	site.mu.Lock()
	site.override["/wiki/api/v2/spaces/12345"] = func(w http.ResponseWriter, r *http.Request) bool {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
		return true
	}
	site.mu.Unlock()
	client := newTestClient(t, site, Credentials{BearerToken: canaryToken})
	if _, err := client.GetSpace(t.Context(), "12345"); err == nil {
		t.Fatal("GetSpace() error = nil, want a refusal for a cross-host redirect")
	}
	other.mu.Lock()
	defer other.mu.Unlock()
	if len(other.hits) != 0 {
		t.Fatalf("the redirect was followed to another host: %v", other.hits)
	}
}

func TestHTTPClientStopsOnARepeatedCursor(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	site.mu.Lock()
	site.override["/wiki/api/v2/spaces/12345/pages"] = func(w http.ResponseWriter, r *http.Request) bool {
		writeJSON(w, map[string]any{
			"results": []Page{samplePage("1001", "A")},
			"_links":  map[string]string{"next": "/wiki/api/v2/spaces/12345/pages?cursor=loop"},
		})
		return true
	}
	site.mu.Unlock()
	client := newTestClient(t, site, Credentials{BearerToken: canaryToken})
	_, truncated, err := client.ListSpacePages(t.Context(), "12345", 10, 1000)
	if err != nil || !truncated {
		t.Fatalf("ListSpacePages() truncated=%v err=%v, want a truncated stop on the repeated cursor", truncated, err)
	}
	if got := len(site.recorded()); got > 3 {
		t.Errorf("requests = %d, want the loop cut after the cursor repeated", got)
	}
}

func TestHTTPClientBoundsAnEndlessCursorChain(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	var mu sync.Mutex
	counter := 0
	site.mu.Lock()
	site.override["/wiki/api/v2/spaces/12345/pages"] = func(w http.ResponseWriter, r *http.Request) bool {
		mu.Lock()
		counter++
		next := counter
		mu.Unlock()
		writeJSON(w, map[string]any{
			"results": []Page{},
			"_links":  map[string]string{"next": fmt.Sprintf("/wiki/api/v2/spaces/12345/pages?cursor=%d", next)},
		})
		return true
	}
	site.mu.Unlock()
	client := newTestClient(t, site, Credentials{BearerToken: canaryToken})
	_, truncated, err := client.ListSpacePages(t.Context(), "12345", 10, 1000)
	if err != nil || !truncated {
		t.Fatalf("ListSpacePages() truncated=%v err=%v, want a truncated stop at the cursor-page backstop", truncated, err)
	}
	if got := len(site.recorded()); got != maxCursorPages {
		t.Errorf("requests = %d, want exactly the %d page backstop", got, maxCursorPages)
	}
}

func TestHTTPClientPageTreeCapCountsTheRoot(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	site.addPage(samplePage("1", "root"))
	for _, id := range []string{"2", "3", "4"} {
		child := samplePage(id, id)
		child.ParentID = "1"
		site.addPage(child)
	}
	client := newTestClient(t, site, Credentials{BearerToken: canaryToken})
	ids, truncated, err := client.ListPageTree(t.Context(), "1", 10, 3)
	if err != nil || len(ids) != 3 || !truncated || ids[0] != "1" {
		t.Fatalf("ListPageTree() = %v truncated=%v err=%v, want the root plus two descendants, truncated", ids, truncated, err)
	}
}

func TestHTTPClientErrorsCarryNoURLOrBody(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	overrideStatus(site, "/wiki/api/v2/spaces/12345", 418, nil)
	client := newTestClient(t, site, Credentials{BearerToken: canaryToken})
	_, err := client.GetSpace(t.Context(), "12345")
	if err == nil {
		t.Fatal("GetSpace() error = nil, want a failure")
	}
	for _, leak := range []string{canaryToken, site.server.URL, "12345", "provider body"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q leaks %q", err, leak)
		}
	}
}

func TestHTTPClientReportsCancellationAsContextError(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	client := newTestClient(t, site, Credentials{BearerToken: canaryToken})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.GetSpace(ctx, "12345"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetSpace() error = %v, want context.Canceled", err)
	}
}
