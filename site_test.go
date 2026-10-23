// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

// canaryToken is a fake credential that must never appear in any output.
const canaryToken = "canary-token-0123456789"

// fakeSite is an in-memory Confluence Cloud v2 API served over httptest. It
// carries synthetic content only.
type fakeSite struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	space    Space
	pages    map[string]Page // by id
	order    []string        // listing order
	children map[string][]string
	denied   map[string]int // page id -> status for GET page
	override map[string]func(http.ResponseWriter, *http.Request) bool
	requests []recordedRequest
}

type recordedRequest struct {
	method string
	path   string
	query  string
	auth   string
}

func newFakeSite(t *testing.T) *fakeSite {
	t.Helper()
	site := &fakeSite{
		t:        t,
		space:    Space{ID: "12345", Key: "ENG", Name: "Engineering"},
		pages:    map[string]Page{},
		children: map[string][]string{},
		denied:   map[string]int{},
		override: map[string]func(http.ResponseWriter, *http.Request) bool{},
	}
	site.server = httptest.NewServer(http.HandlerFunc(site.serve))
	t.Cleanup(site.server.Close)
	return site
}

func (s *fakeSite) baseURL() string { return s.server.URL + "/wiki" }

func (s *fakeSite) addPage(page Page) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if page.Status == "" {
		page.Status = "current"
	}
	if page.SpaceID == "" {
		page.SpaceID = s.space.ID
	}
	if page.Version.Number == 0 {
		page.Version.Number = 1
	}
	if page.Version.CreatedAt == "" {
		page.Version.CreatedAt = "2026-09-01T10:00:00.000Z"
	}
	if page.Body.Storage.Representation == "" {
		page.Body.Storage.Representation = "storage"
	}
	if page.Links.WebUI == "" {
		page.Links.WebUI = "/spaces/ENG/pages/" + page.ID
	}
	if _, exists := s.pages[page.ID]; !exists {
		s.order = append(s.order, page.ID)
	}
	s.pages[page.ID] = page
	if page.ParentID != "" {
		s.children[page.ParentID] = append(s.children[page.ParentID], page.ID)
	}
}

func (s *fakeSite) recorded() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedRequest(nil), s.requests...)
}

func (s *fakeSite) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, recordedRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")})
	handler := s.override[r.URL.Path]
	s.mu.Unlock()
	if handler != nil && handler(w, r) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/wiki")
	switch {
	case r.Method != http.MethodGet:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	case path == "/api/v2/spaces/"+s.space.ID:
		writeJSON(w, s.space)
	case path == "/api/v2/spaces/"+s.space.ID+"/pages":
		s.listPages(w, r)
	case strings.HasPrefix(path, "/api/v2/pages/") && strings.HasSuffix(path, "/descendants"):
		s.listDescendants(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/api/v2/pages/"), "/descendants"))
	case strings.HasPrefix(path, "/api/v2/pages/"):
		s.getPage(w, strings.TrimPrefix(path, "/api/v2/pages/"))
	default:
		http.NotFound(w, r)
	}
}

func (s *fakeSite) listPages(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	start, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	end := start + limit
	if end > len(s.order) {
		end = len(s.order)
	}
	results := make([]Page, 0, end-start)
	for _, id := range s.order[start:end] {
		listed := s.pages[id]
		listed.Labels = labelResults{}
		results = append(results, listed)
	}
	response := map[string]any{"results": results, "_links": map[string]string{}}
	if end < len(s.order) {
		response["_links"] = map[string]string{
			"next": fmt.Sprintf("/wiki/api/v2/spaces/%s/pages?cursor=%d&limit=%d", s.space.ID, end, limit),
		}
	}
	writeJSON(w, response)
}

func (s *fakeSite) listDescendants(w http.ResponseWriter, r *http.Request, root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []string
	var walk func(string)
	walk = func(id string) {
		for _, child := range s.children[id] {
			all = append(all, child)
			walk(child)
		}
	}
	walk(root)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	start, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	type descendant struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	results := []descendant{}
	for _, id := range all[start:end] {
		results = append(results, descendant{ID: id, Type: "page"})
	}
	links := map[string]string{}
	if end < len(all) {
		links["next"] = fmt.Sprintf("/wiki/api/v2/pages/%s/descendants?cursor=%d&limit=%d", root, end, limit)
	}
	writeJSON(w, map[string]any{"results": results, "_links": links})
}

func (s *fakeSite) getPage(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status := s.denied[id]; status != 0 {
		http.Error(w, "denied", status)
		return
	}
	page, ok := s.pages[id]
	if !ok {
		http.NotFound(w, nil)
		return
	}
	writeJSON(w, page)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// samplePage returns a synthetic page whose body links to an external site and
// to another page.
func samplePage(id, title string) Page {
	return Page{
		ID:    id,
		Title: title,
		Body: PageBody{Storage: BodyRepresentation{
			Value: `<p>See <a href="https://docs.example.invalid/guide">the guide</a> and ` +
				`<ac:link><ri:page ri:content-id="2002"/><ac:plain-text-link-body>Other</ac:plain-text-link-body></ac:link>.</p>`,
		}},
		Labels:  labelResults{Results: []Label{{Name: "runbook"}}},
		OwnerID: "user-1",
	}
}

// testClaim is a claim for the default synthetic space.
func testClaim() sdk.Claim {
	return sdk.Claim{
		ComponentID:   ComponentID,
		InstanceID:    "confluence-eng",
		CollectorKind: CollectorKind,
		SourceSystem:  SourceSystem,
		Scope:         sdk.Scope{ID: "doc-source:confluence:confluence.example.invalid:12345", Kind: "documentation_source"},
		SourceRunID:   "component-generation:abc",
		GenerationID:  "component-generation:abc",
		WorkItemID:    "component-work:abc",
		FencingToken:  "7",
		Attempt:       1,
		Deadline:      testNow().Add(time.Hour),
		ConfigHandle:  "component-config:confluence",
	}
}

func testNow() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }

func fixedClock() func() time.Time { return testNow }

// testConfig is a space-mode config against the fake site.
func testConfig(site *fakeSite) Config {
	return Config{
		BaseURL:          site.baseURL(),
		SpaceID:          site.space.ID,
		PageLimit:        100,
		MaxTotalPages:    5000,
		Credentials:      Credentials{Email: "bot@example.invalid", APIToken: canaryToken},
		ClaimTimeout:     time.Minute,
		FetchConcurrency: 4,
		MaxPageBytes:     defaultMaxPageBytes,
		MaxResultBytes:   defaultMaxResultBytes,
		MaxResponseBytes: defaultMaxResponseBytes,
	}
}

func collectOnce(t *testing.T, site *fakeSite, cfg Config, claim sdk.Claim) Outcome {
	t.Helper()
	collector := &Collector{Config: cfg, Clock: fixedClock()}
	return collector.Collect(t.Context(), claim)
}

// kindCounts counts result facts by kind.
func kindCounts(result sdk.Result) map[string]int {
	counts := map[string]int{}
	for _, fact := range result.Facts {
		counts[fact.Kind]++
	}
	return counts
}

func sortedKinds(counts map[string]int) []string {
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}
