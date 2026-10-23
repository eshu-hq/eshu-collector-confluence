// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

func TestCollectStripsNULAndInvalidUTF8(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	page := samplePage("7001", "Title\x00with\x00nul \xff\xfe")
	page.Body.Storage.Value = "<p>body\x00text \xc3\x28 <a href=\"https://docs.example.invalid/a\x00b\">link</a></p>"
	page.Labels = labelResults{Results: []Label{{Name: "la\x00bel"}}}
	site.addPage(page)
	outcome := collectOnce(t, site, testConfig(site), testClaim())

	if outcome.State != sdk.ResultComplete {
		t.Fatalf("State = %q, want complete", outcome.State)
	}
	assertHostAccepts(t, outcome.Result)
	raw, _ := json.Marshal(outcome)
	if strings.Contains(string(raw), `\u0000`) {
		t.Fatalf("result carries a NUL escape the store cannot hold: %s", raw)
	}
	for _, fact := range outcome.Facts {
		if fact.Kind == FactKindDocument && fact.Payload["title"] != "Titlewithnul ��" {
			t.Errorf("title = %q, want NULs dropped and invalid bytes replaced", fact.Payload["title"])
		}
	}
}

func TestCollectKeepsOtherControlCharactersEscaped(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	page := samplePage("7002", "Tab\there\x07bell")
	site.addPage(page)
	outcome := collectOnce(t, site, testConfig(site), testClaim())
	assertHostAccepts(t, outcome.Result)
	for _, fact := range outcome.Facts {
		if fact.Kind == FactKindDocument && fact.Payload["title"] != "Tab\there\x07bell" {
			t.Errorf("title = %q, want other control characters preserved", fact.Payload["title"])
		}
	}
}

func TestCollectReplacesHostileWebLinksWithTheRESTURI(t *testing.T) {
	t.Parallel()

	for name, webui := range map[string]string{
		"userinfo":        "https://user:hunter2@evil.example.invalid/page",
		"file path":       "file:///etc/passwd",
		"javascript":      "javascript:alert(1)",
		"sensitive query": "https://docs.example.invalid/page?api_key=abc",
		"control char":    "https://docs.example.invalid/pa\x01ge",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			site := newFakeSite(t)
			page := samplePage("7003", "Hostile link")
			page.Links.WebUI = webui
			site.addPage(page)
			outcome := collectOnce(t, site, testConfig(site), testClaim())
			if outcome.State != sdk.ResultComplete {
				t.Fatalf("State = %q, want complete; a hostile link must not fail the claim", outcome.State)
			}
			assertHostAccepts(t, outcome.Result)
			for _, fact := range outcome.Facts {
				if fact.Kind == FactKindDocument {
					want := site.baseURL() + "/api/v2/pages/7003"
					if fact.SourceRef.URI != want || fact.Payload["canonical_uri"] != want {
						t.Errorf("uri = %q canonical = %v, want the REST fallback %q", fact.SourceRef.URI, fact.Payload["canonical_uri"], want)
					}
				}
			}
			raw, _ := json.Marshal(outcome)
			if strings.Contains(string(raw), "hunter2") {
				t.Fatal("a credential embedded in a provider link reached the result")
			}
		})
	}
}

func TestCollectRelativeWebLinkResolvesAgainstTheBase(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	site.addPage(samplePage("7004", "Relative"))
	outcome := collectOnce(t, site, testConfig(site), testClaim())
	for _, fact := range outcome.Facts {
		if fact.Kind == FactKindDocument {
			want := site.server.URL + "/spaces/ENG/pages/7004"
			if got := fact.Payload["canonical_uri"]; got != want {
				t.Errorf("canonical_uri = %v, want %q", got, want)
			}
		}
	}
}

func TestCollectMalformedStorageFormatStillYieldsAPage(t *testing.T) {
	t.Parallel()

	bodies := map[string]string{
		"unclosed tags":     "<p><b>bold <i>italic <a href='https://docs.example.invalid/x'>link",
		"stray close":       "</div></p></a>text",
		"bad entities":      "<p>&notanentity; &#xZZ; &#99999999999;</p>",
		"binary":            "\x01\x02\x03<<<>>>&&&",
		"empty":             "",
		"only whitespace":   "   \n\t  ",
		"unterminated attr": "<a href=\"https://docs.example.invalid/x>text</a>",
		"nested ac link":    "<ac:link><ac:link><ri:page ri:content-id=\"9\"/></ac:link></ac:link>",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			site := newFakeSite(t)
			page := samplePage("7005", "Malformed")
			page.Body.Storage.Value = body
			site.addPage(page)
			outcome := collectOnce(t, site, testConfig(site), testClaim())
			if outcome.State != sdk.ResultComplete {
				t.Fatalf("State = %q, want complete", outcome.State)
			}
			assertHostAccepts(t, outcome.Result)
			if kindCounts(outcome.Result)[FactKindDocument] != 1 || kindCounts(outcome.Result)[FactKindSection] != 1 {
				t.Errorf("counts = %v, want one document and one section", kindCounts(outcome.Result))
			}
		})
	}
}

func TestCollectDeeplyNestedBodyDoesNotExhaustTheStack(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	page := samplePage("7006", "Deep")
	page.Body.Storage.Value = strings.Repeat("<div>", 200_000) + `<a href="https://docs.example.invalid/deep">deep</a>`
	site.addPage(page)
	cfg := testConfig(site)
	cfg.MaxPageBytes = 4 << 20
	cfg.MaxResultBytes = 8 << 20
	outcome := collectOnce(t, site, cfg, testClaim())
	if outcome.State != sdk.ResultComplete && outcome.State != sdk.ResultPartial {
		t.Fatalf("State = %q, want a bounded result", outcome.State)
	}
}

func TestCollectSkipsPagesOverThePageByteCap(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	small := samplePage("7101", "Small")
	huge := samplePage("7102", "Huge")
	huge.Body.Storage.Value = "<p>" + strings.Repeat("x", 4096) + "</p>"
	site.addPage(small)
	site.addPage(huge)
	cfg := testConfig(site)
	cfg.MaxPageBytes = 1024
	outcome := collectOnce(t, site, cfg, testClaim())

	if outcome.State != sdk.ResultPartial {
		t.Fatalf("State = %q, want partial when a page is skipped", outcome.State)
	}
	assertHostAccepts(t, outcome.Result)
	for _, fact := range outcome.Facts {
		if fact.SourceRef.RecordID == "7102" {
			t.Fatal("an oversize page must be skipped whole, never truncated")
		}
	}
	metadata := outcome.Facts[0].Payload["source_metadata"].(map[string]any)
	if metadata["failure_count"] != "1" || metadata["page_count"] != "1" {
		t.Errorf("source_metadata = %v, want one skipped page and one included", metadata)
	}
}

func TestCollectKeepsTheResultUnderTheByteBudget(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	for i := 0; i < 40; i++ {
		page := samplePage(fmt.Sprintf("%04d", 8000+i), "Page")
		page.Body.Storage.Value = "<p>" + strings.Repeat("filler ", 400) + "</p>"
		site.addPage(page)
	}
	cfg := testConfig(site)
	cfg.MaxResultBytes = 3*resultOverheadBytes + sourceReserveBytes + 60_000
	outcome := collectOnce(t, site, cfg, testClaim())

	if outcome.State != sdk.ResultPartial {
		t.Fatalf("State = %q, want partial when the budget cuts pages", outcome.State)
	}
	assertHostAccepts(t, outcome.Result)
	raw, _ := json.Marshal(outcome)
	if len(raw) > cfg.MaxResultBytes {
		t.Fatalf("result is %d bytes, over the %d byte budget", len(raw), cfg.MaxResultBytes)
	}
	included := kindCounts(outcome.Result)[FactKindDocument]
	if included == 0 || included >= 40 {
		t.Fatalf("included pages = %d, want some but not all of 40", included)
	}
	metadata := outcome.Facts[0].Payload["source_metadata"].(map[string]any)
	if metadata["coverage_warning"] != "truncated" {
		t.Errorf("coverage_warning = %v, want truncated", metadata["coverage_warning"])
	}
	// Pages are included whole: every document has its section.
	if kindCounts(outcome.Result)[FactKindSection] != included {
		t.Errorf("sections = %d, documents = %d, want a section for every document", kindCounts(outcome.Result)[FactKindSection], included)
	}
	// The same budget cuts the same pages on a rerun.
	again, _ := json.Marshal(collectOnce(t, site, cfg, testClaim()))
	if string(again) != string(raw) {
		t.Error("the budget cut different pages on a rerun")
	}
}

func TestCollectManyLinksAreBoundedByTheBudget(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	page := samplePage("8100", "Link farm")
	var body strings.Builder
	for i := 0; i < 20_000; i++ {
		fmt.Fprintf(&body, `<a href="https://docs.example.invalid/%d">l</a>`, i)
	}
	page.Body.Storage.Value = body.String()
	site.addPage(page)
	site.addPage(samplePage("8101", "Normal"))
	cfg := testConfig(site)
	cfg.MaxPageBytes = 2 << 20
	outcome := collectOnce(t, site, cfg, testClaim())
	assertHostAccepts(t, outcome.Result)
	raw, _ := json.Marshal(outcome)
	if len(raw) > cfg.MaxResultBytes {
		t.Fatalf("result is %d bytes, over the %d byte budget", len(raw), cfg.MaxResultBytes)
	}
	if outcome.State != sdk.ResultPartial {
		t.Fatalf("State = %q, want partial when the link farm is cut", outcome.State)
	}
	for _, fact := range outcome.Facts {
		if fact.SourceRef.RecordID == "8100" {
			t.Fatal("the over-budget page must be left out whole")
		}
	}
	if kindCounts(outcome.Result)[FactKindDocument] != 1 {
		t.Error("the later, smaller page must still be included")
	}
}

func TestCollectRejectsAnOversizeProviderResponse(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	site.mu.Lock()
	site.override["/wiki/api/v2/spaces/12345"] = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"12345","name":%q}`, strings.Repeat("n", 4096))
		return true
	}
	site.mu.Unlock()
	cfg := testConfig(site)
	cfg.MaxResponseBytes = 1024
	outcome := collectOnce(t, site, cfg, testClaim())
	if outcome.State != sdk.ResultTerminal || failureClassOf(t, outcome.Result) != FailureResponseTooLarge {
		t.Fatalf("state %q class %q, want terminal response-too-large", outcome.State, failureClassOf(t, outcome.Result))
	}
}

func TestCollectGarbageResponsesAreTerminalNotRetried(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"html error page": "<html>gateway</html>",
		"empty body":      "",
		"truncated json":  `{"id":"12345","na`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			site := twoPageSite(t)
			site.mu.Lock()
			site.override["/wiki/api/v2/spaces/12345"] = func(w http.ResponseWriter, r *http.Request) bool {
				_, _ = w.Write([]byte(body))
				return true
			}
			site.mu.Unlock()
			outcome := collectOnce(t, site, testConfig(site), testClaim())
			if outcome.State != sdk.ResultTerminal || failureClassOf(t, outcome.Result) != FailureInvalidResponse {
				t.Fatalf("state %q class %q, want terminal invalid-response", outcome.State, failureClassOf(t, outcome.Result))
			}
		})
	}
}
