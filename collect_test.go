// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
	"github.com/eshu-hq/eshu/sdk/go/collector/conformance"
	"github.com/eshu-hq/eshu/sdk/go/factschema/fixturepack"
)

// assertHostAccepts runs the checks the host runs on every result: the SDK
// contract validator and the declared payload schemas.
func assertHostAccepts(t *testing.T, result sdk.Result) {
	t.Helper()
	if _, err := sdk.NewValidator(Contract()).ValidateResult(result); err != nil {
		t.Fatalf("ValidateResult() error = %v", err)
	}
	schemas := map[string]json.RawMessage{}
	for _, kind := range []string{FactKindSource, FactKindDocument, FactKindSection, FactKindLink} {
		raw, ok := fixturepack.SchemaFor(kind)
		if !ok {
			t.Fatalf("fixturepack has no schema for %s", kind)
		}
		schemas[kind] = raw
	}
	if err := conformance.ValidatePayloadSchemas(schemas, result); err != nil {
		t.Fatalf("ValidatePayloadSchemas() error = %v", err)
	}
}

func twoPageSite(t *testing.T) *fakeSite {
	t.Helper()
	site := newFakeSite(t)
	site.addPage(samplePage("1001", "Runbook"))
	child := samplePage("1002", "Child")
	child.ParentID = "1001"
	site.addPage(child)
	return site
}

func TestCollectCompleteSpaceSnapshot(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	claim := testClaim()
	outcome := collectOnce(t, site, testConfig(site), claim)

	if outcome.State != sdk.ResultComplete {
		t.Fatalf("State = %q, want complete; statuses = %#v", outcome.State, outcome.Statuses)
	}
	assertHostAccepts(t, outcome.Result)
	counts := kindCounts(outcome.Result)
	want := map[string]int{FactKindSource: 1, FactKindDocument: 2, FactKindSection: 2, FactKindLink: 4}
	for kind, n := range want {
		if counts[kind] != n {
			t.Errorf("%s facts = %d, want %d (all counts %v)", kind, counts[kind], n, counts)
		}
	}
	if outcome.Claim != claim {
		t.Errorf("Claim = %#v, want the host claim echoed unchanged", outcome.Claim)
	}
	if outcome.Generation.ID != claim.GenerationID {
		t.Errorf("Generation.ID = %q, want the claim generation %q", outcome.Generation.ID, claim.GenerationID)
	}
	if outcome.Generation.FreshnessHint == "" {
		t.Error("FreshnessHint is empty; the core store needs it to skip duplicates")
	}
	if outcome.TrustedDeletion {
		t.Error("TrustedDeletion = true without the operator attestation")
	}
	for _, fact := range outcome.Facts {
		ref := fact.SourceRef
		if ref.ScopeID != claim.Scope.ID || ref.GenerationID != claim.GenerationID || ref.SourceSystem != claim.SourceSystem {
			t.Errorf("%s source_ref = %#v, want the claim scope, generation, and source system", fact.Kind, ref)
		}
		if fact.SourceConfidence != sdk.SourceConfidenceObserved {
			t.Errorf("%s confidence = %q, want observed", fact.Kind, fact.SourceConfidence)
		}
	}
	source := outcome.Facts[0]
	if source.Kind != FactKindSource {
		t.Fatalf("first fact kind = %q, want the source fact", source.Kind)
	}
	if got := source.Payload["source_id"]; got != claim.Scope.ID {
		t.Errorf("source payload source_id = %v, want the claim scope id", got)
	}
}

func TestCollectPageTreeScopeReadsRootAndDescendants(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	cfg := testConfig(site)
	cfg.SpaceID, cfg.RootPageID, cfg.SpaceKey = "", "1001", "ENG"
	outcome := collectOnce(t, site, cfg, testClaim())

	if outcome.State != sdk.ResultComplete {
		t.Fatalf("State = %q, want complete", outcome.State)
	}
	assertHostAccepts(t, outcome.Result)
	source := outcome.Facts[0].Payload
	if source["source_type"] != "confluence_page_tree" {
		t.Errorf("source_type = %v, want confluence_page_tree", source["source_type"])
	}
	if source["external_id"] != "12345" {
		t.Errorf("external_id = %v, want the space id of the first page", source["external_id"])
	}
	if kindCounts(outcome.Result)[FactKindDocument] != 2 {
		t.Errorf("documents = %d, want root plus one descendant", kindCounts(outcome.Result)[FactKindDocument])
	}
	for _, request := range site.recorded() {
		if strings.Contains(request.path, "/spaces/") {
			t.Errorf("page-tree mode requested a space endpoint: %s", request.path)
		}
	}
}

func TestCollectSpaceModeNeverReadsOutsideTheSpace(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	collectOnce(t, site, testConfig(site), testClaim())
	for _, request := range site.recorded() {
		if request.method != "GET" {
			t.Errorf("request method = %s, want GET only", request.method)
		}
		if strings.Contains(request.path, "/descendants") {
			t.Errorf("space mode requested a page-tree endpoint: %s", request.path)
		}
	}
}

func TestCollectPaginatesAndRespectsMaxTotalPages(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	for _, id := range []string{"2001", "2002", "2003", "2004", "2005"} {
		site.addPage(samplePage(id, "Page "+id))
	}
	cfg := testConfig(site)
	cfg.PageLimit = 2

	full := collectOnce(t, site, cfg, testClaim())
	if full.State != sdk.ResultComplete || kindCounts(full.Result)[FactKindDocument] != 5 {
		t.Fatalf("paginated read: state %q documents %d, want complete with 5", full.State, kindCounts(full.Result)[FactKindDocument])
	}
	listCalls := 0
	for _, request := range site.recorded() {
		if strings.HasSuffix(request.path, "/pages") {
			listCalls++
		}
	}
	if listCalls != 3 {
		t.Errorf("list calls = %d, want 3 for 5 pages at limit 2", listCalls)
	}

	cfg.MaxTotalPages = 3
	capped := collectOnce(t, site, cfg, testClaim())
	if capped.State != sdk.ResultPartial {
		t.Fatalf("capped read: state = %q, want partial", capped.State)
	}
	assertHostAccepts(t, capped.Result)
	if got := kindCounts(capped.Result)[FactKindDocument]; got != 3 {
		t.Errorf("capped documents = %d, want 3", got)
	}
	metadata := capped.Facts[0].Payload["source_metadata"].(map[string]any)
	if metadata["coverage_warning"] != "truncated" || metadata["max_total_pages"] != "3" || metadata["page_count"] != "3" {
		t.Errorf("source_metadata = %v, want coverage_warning truncated, max_total_pages 3, page_count 3", metadata)
	}
}

func TestCollectMaxTotalPagesExactFitIsNotTruncated(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	for _, id := range []string{"3001", "3002", "3003"} {
		site.addPage(samplePage(id, "Page "+id))
	}
	cfg := testConfig(site)
	cfg.PageLimit, cfg.MaxTotalPages = 2, 3
	outcome := collectOnce(t, site, cfg, testClaim())
	if outcome.State != sdk.ResultComplete {
		t.Fatalf("State = %q, want complete when the cap equals the page count", outcome.State)
	}
}

func TestCollectSkipsUnreadablePageAsPartial(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	site.denied["1002"] = 403
	outcome := collectOnce(t, site, testConfig(site), testClaim())

	if outcome.State != sdk.ResultPartial {
		t.Fatalf("State = %q, want partial", outcome.State)
	}
	assertHostAccepts(t, outcome.Result)
	if got := kindCounts(outcome.Result)[FactKindDocument]; got != 1 {
		t.Errorf("documents = %d, want only the readable page", got)
	}
	metadata := outcome.Facts[0].Payload["source_metadata"].(map[string]any)
	if metadata["sync_status"] != "partial" || metadata["failure_count"] != "1" {
		t.Errorf("source_metadata = %v, want sync_status partial and failure_count 1", metadata)
	}
	status := outcome.Statuses[0]
	if !status.Partial || status.WarningCount != 1 {
		t.Errorf("status = %#v, want a partial warning with one warning", status)
	}
}

func TestCollectAclSummaryStaysPartialNeverAllowed(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	outcome := collectOnce(t, site, testConfig(site), testClaim())
	for _, fact := range outcome.Facts {
		if fact.Kind != FactKindSource && fact.Kind != FactKindDocument {
			continue
		}
		acl, ok := fact.Payload["acl_summary"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no acl_summary", fact.Kind)
		}
		if acl["source_acl_state"] != "partial" || acl["is_partial"] != true || acl["visibility"] != "credential_viewable" {
			t.Errorf("%s acl_summary = %v, want partial, never upgraded to allowed", fact.Kind, acl)
		}
	}
}

func TestCollectEmptySpaceEmitsOnlyTheSourceFact(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	outcome := collectOnce(t, site, testConfig(site), testClaim())
	if outcome.State != sdk.ResultComplete {
		t.Fatalf("State = %q, want complete for an empty space", outcome.State)
	}
	assertHostAccepts(t, outcome.Result)
	if len(outcome.Facts) != 1 || outcome.Facts[0].Kind != FactKindSource {
		t.Fatalf("facts = %d, want exactly the source fact", len(outcome.Facts))
	}
	if outcome.TrustedDeletion {
		t.Error("an empty successor must not be attested unless the operator says so")
	}
}

func TestCollectLatestCurrentVersionWins(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	current := samplePage("4001", "Current")
	current.Version.Number = 5
	site.addPage(current)
	site.mu.Lock()
	site.order = append(site.order, "4002")
	site.pages["4002"] = Page{ID: "4002", Status: "trashed", Title: "Gone", SpaceID: "12345", Version: PageVersion{Number: 9}}
	site.mu.Unlock()
	outcome := collectOnce(t, site, testConfig(site), testClaim())
	if got := kindCounts(outcome.Result)[FactKindDocument]; got != 1 {
		t.Fatalf("documents = %d, want only the current page", got)
	}
	var document map[string]any
	for _, fact := range outcome.Facts {
		if fact.Kind == FactKindDocument {
			document = fact.Payload
		}
	}
	if document["revision_id"] != "5" || document["external_id"] != "4001" {
		t.Errorf("document = %v, want revision 5 of page 4001", document)
	}
}
