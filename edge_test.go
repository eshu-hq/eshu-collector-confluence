// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"net/http"
	"strings"
	"testing"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

// sourceFactOf returns the one documentation_source fact of a result.
func sourceFactOf(t *testing.T, result sdk.Result) sdk.Fact {
	t.Helper()
	for _, fact := range result.Facts {
		if fact.Kind == FactKindSource {
			return fact
		}
	}
	t.Fatalf("result %q has no documentation_source fact", result.State)
	return sdk.Fact{}
}

// TestSourceFactDoesNotBorrowTheConfiguredSpaceID pins review finding F9. The
// in-tree collector names the source with the space response's own id, name,
// and key, falling back only to the root page id. It never uses the configured
// space id, and the external id feeds the source stable key, so borrowing it
// would store a different source than the in-tree collector did when the space
// response carries no identity.
func TestSourceFactDoesNotBorrowTheConfiguredSpaceID(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	site.mu.Lock()
	site.override["/wiki/api/v2/spaces/12345"] = func(w http.ResponseWriter, _ *http.Request) bool {
		writeJSON(w, map[string]any{})
		return true
	}
	site.mu.Unlock()
	outcome := collectOnce(t, site, testConfig(site), testClaim())
	if outcome.State != sdk.ResultComplete {
		t.Fatalf("state %q class %q, want complete", outcome.State, failureClassOf(t, outcome))
	}
	payload := sourceFactOf(t, outcome).Payload
	if got, _ := payload["external_id"].(string); got != "" {
		t.Errorf("external_id = %q, want empty (the space response had no id)", got)
	}
	if got, _ := payload["display_name"].(string); got != "" {
		t.Errorf("display_name = %q, want empty (the space response had no name or key)", got)
	}
}

// TestListedPageWithoutAnIDIsDroppedBeforeAnyDetailRead pins review mutant M27.
// A listed page with a blank id cannot be identified, so it is left out without
// a detail request (GET .../pages/ with no id) and without counting as a
// skipped page: the snapshot stays complete.
func TestListedPageWithoutAnIDIsDroppedBeforeAnyDetailRead(t *testing.T) {
	t.Parallel()

	for name, id := range map[string]string{"empty": "", "blank": "   "} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			site := twoPageSite(t)
			site.mu.Lock()
			site.order = append(site.order, id)
			site.pages[id] = samplePage(id, "No identity")
			site.mu.Unlock()
			outcome := collectOnce(t, site, testConfig(site), testClaim())
			if outcome.State != sdk.ResultComplete {
				t.Fatalf("state %q, want complete: a page without an id is not a skipped page", outcome.State)
			}
			if got := outcome.Statuses[0].WarningCount; got != 0 {
				t.Errorf("WarningCount = %d, want 0", got)
			}
			if got := kindCounts(outcome)[FactKindDocument]; got != 2 {
				t.Errorf("document facts = %d, want the 2 identified pages", got)
			}
			for _, request := range site.recorded() {
				rest, isDetail := strings.CutPrefix(request.path, "/wiki/api/v2/pages/")
				if isDetail && strings.TrimSpace(rest) == "" {
					t.Errorf("unexpected detail request %q for a page with no id", request.path)
				}
			}
		})
	}
}
