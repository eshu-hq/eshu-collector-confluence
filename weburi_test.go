// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

// TestCanonicalURIJoinsTheWebUILinkToItsBase pins the join rule for a page's
// browsable URL. Confluence Cloud documents `_links.base` as the site URL with
// the /wiki context path and `_links.webui` as a root-relative path, so the two
// are concatenated; RFC 3986 resolution would replace the base path and drop
// /wiki (eshu-hq/eshu#8913).
func TestCanonicalURIJoinsTheWebUILinkToItsBase(t *testing.T) {
	t.Parallel()

	const configured = "https://config.example.invalid/wiki"
	rest := func(base string) string { return base + "/api/v2/pages/1001" }

	tests := []struct {
		name  string
		base  string // _links.base; empty falls back to the configured base URL
		webui string
		want  string
	}{
		{"cloud base with wiki", "https://acme.example.invalid/wiki", "/spaces/ENG/pages/1001/Runbook", "https://acme.example.invalid/wiki/spaces/ENG/pages/1001/Runbook"},
		{"base without wiki", "https://acme.example.invalid", "/spaces/ENG/pages/1001/Runbook", "https://acme.example.invalid/spaces/ENG/pages/1001/Runbook"},
		{"trailing slash on base", "https://acme.example.invalid/wiki/", "/spaces/ENG/pages/1001", "https://acme.example.invalid/wiki/spaces/ENG/pages/1001"},
		{"webui without leading slash", "https://acme.example.invalid/wiki", "spaces/ENG/pages/1001", "https://acme.example.invalid/wiki/spaces/ENG/pages/1001"},
		{"webui already starts with the base path", "https://acme.example.invalid/wiki", "/wiki/spaces/ENG/pages/1001", "https://acme.example.invalid/wiki/spaces/ENG/pages/1001"},
		{"webui equal to the base path", "https://acme.example.invalid/wiki", "/wiki", "https://acme.example.invalid/wiki"},
		{"webui that only shares a prefix with the base path", "https://acme.example.invalid/wiki", "/wikipedia/pages/1001", "https://acme.example.invalid/wiki/wikipedia/pages/1001"},
		{"configured base when the page has none", "", "/spaces/ENG/pages/1001", "https://config.example.invalid/wiki/spaces/ENG/pages/1001"},
		{"query and fragment", "https://acme.example.invalid/wiki", "/spaces/ENG/pages/1001?focusedCommentId=5#comment-5", "https://acme.example.invalid/wiki/spaces/ENG/pages/1001?focusedCommentId=5#comment-5"},
		{"escaped path is kept", "https://acme.example.invalid/wiki", "/display/TST/Test%20Page+a%2Fb", "https://acme.example.invalid/wiki/display/TST/Test%20Page+a%2Fb"},
		{"empty webui falls back to the REST URI", "https://acme.example.invalid/wiki", "", rest("https://acme.example.invalid/wiki")},
		{"blank webui falls back to the REST URI", "https://acme.example.invalid/wiki", "  ", rest("https://acme.example.invalid/wiki")},
		{"absolute webui is used as is", "https://acme.example.invalid/wiki", "https://docs.example.invalid/pages/1001", "https://docs.example.invalid/pages/1001"},
		{"scheme-relative webui falls back to the REST URI", "https://acme.example.invalid/wiki", "//other.example.invalid/pages/1001", rest(configured)},
		{"javascript webui falls back to the REST URI", "https://acme.example.invalid/wiki", "javascript:alert(1)", rest(configured)},
		{"file webui falls back to the REST URI", "https://acme.example.invalid/wiki", "file:///etc/passwd", rest(configured)},
		{"unparseable webui falls back to the REST URI", "https://acme.example.invalid/wiki", "http://[::1", rest(configured)},
		{"credential in webui falls back to the REST URI", "https://acme.example.invalid/wiki", "https://user:hunter2@acme.example.invalid/pages/1001", rest(configured)},
		{"credential in base link falls back to the REST URI", "https://user:hunter2@acme.example.invalid/wiki", "/spaces/ENG/pages/1001", rest(configured)},
		{"sensitive query falls back to the REST URI", "https://acme.example.invalid/wiki", "/spaces/ENG/pages/1001?api_key=abc", rest(configured)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			page := Page{ID: "1001", Links: Links{Base: test.base, WebUI: test.webui}}
			if got := canonicalURI(configured, page); got != test.want {
				t.Errorf("canonicalURI(base %q, webui %q) = %q, want %q", test.base, test.webui, got, test.want)
			}
		})
	}
}

// TestCloudPageFactsCarryTheWikiContextPath runs a Cloud-shaped page response
// through a full claim and checks every page fact. The fixture is synthetic: a
// base ending in /wiki and a root-relative webui, as Atlassian documents.
func TestCloudPageFactsCarryTheWikiContextPath(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("testdata", "cloud", "page-by-id.json"))
	if err != nil {
		t.Fatal(err)
	}
	var page Page
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode the Cloud fixture: %v", err)
	}
	const want = "https://acme.example.invalid/wiki/spaces/ENG/pages/1001/Platform+Runbook"
	if page.Links.Base != "https://acme.example.invalid/wiki" || !strings.HasPrefix(page.Links.WebUI, "/spaces/") {
		t.Fatalf("fixture is not Cloud-shaped: %+v", page.Links)
	}

	site := newFakeSite(t)
	site.addPage(page)
	outcome := collectOnce(t, site, testConfig(site), testClaim())
	if outcome.State != sdk.ResultComplete {
		t.Fatalf("State = %q, want complete", outcome.State)
	}
	assertHostAccepts(t, outcome)

	pageFacts := 0
	for _, fact := range outcome.Facts {
		if fact.Kind == FactKindSource {
			continue
		}
		pageFacts++
		if fact.SourceRef.URI != want {
			t.Errorf("%s %s source_ref uri = %q, want %q", fact.Kind, fact.StableKey, fact.SourceRef.URI, want)
		}
		if uri, ok := fact.Payload["canonical_uri"]; ok && uri != want {
			t.Errorf("%s %s canonical_uri = %v, want %q", fact.Kind, fact.StableKey, uri, want)
		}
	}
	if counts := kindCounts(outcome); counts[FactKindDocument] != 1 || pageFacts < 3 {
		t.Fatalf("fact counts = %v, want one document and its sections and links", counts)
	}
}
