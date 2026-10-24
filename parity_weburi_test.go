// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// webUIFix pairs the URL this collector emits for a page with the URL the
// in-tree collector recorded in the golden.
type webUIFix struct {
	expected string
	inTree   string
}

// webUIFixes returns, for each page in a recorded scenario whose `_links.webui`
// is a root-relative path, the URL this collector must emit and the URL the
// in-tree collector recorded. This collector concatenates the provider base
// (`_links.base`, else the configured base) and the path; the in-tree collector
// resolves the path against the base with RFC 3986 rules, which drops the
// base path (/wiki on Cloud). The difference is listed in testdata/README.md.
// A page whose base has no path joins the same either way and has no entry.
func webUIFixes(t *testing.T, scenario recordedScenario) map[string]webUIFix {
	t.Helper()
	fixes := map[string]webUIFix{}
	var visit func(value any)
	visit = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			id, _ := typed["id"].(string)
			links, _ := typed["_links"].(map[string]any)
			webui, _ := links["webui"].(string)
			if id != "" && strings.HasPrefix(webui, "/") && !strings.HasPrefix(webui, "//") {
				base, _ := links["base"].(string)
				base = strings.TrimRight(firstNonEmpty(base, scenario.Config.BaseURL), "/")
				parsed, err := url.Parse(base)
				if err != nil {
					t.Fatalf("parse base %q: %v", base, err)
				}
				fix := webUIFix{expected: base + webui, inTree: parsed.Scheme + "://" + parsed.Host + webui}
				if previous, seen := fixes[id]; seen && previous != fix {
					t.Fatalf("page %s is recorded with two different web UI links: %+v and %+v", id, previous, fix)
				}
				if fix.expected != fix.inTree {
					fixes[id] = fix
				}
			}
			for _, child := range typed {
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	for _, exchange := range scenario.Exchanges {
		var body any
		if len(exchange.Body) > 0 && json.Unmarshal(exchange.Body, &body) == nil {
			visit(body)
		}
	}
	return fixes
}

// applyWebUIFix rewrites an in-tree golden fact to the URL this collector emits,
// after proving the golden holds the in-tree form. The goldens stay as the
// in-tree collector recorded them.
func applyWebUIFix(t *testing.T, fact goldenFact, fix webUIFix) goldenFact {
	t.Helper()
	if fact.SourceRef.SourceURI != fix.inTree {
		t.Fatalf("golden source_uri %q is not the in-tree form %q", fact.SourceRef.SourceURI, fix.inTree)
	}
	fact.SourceRef.SourceURI = fix.expected
	if fact.Kind == FactKindDocument {
		payload := map[string]any{}
		for key, value := range fact.Payload {
			payload[key] = value
		}
		if payload["canonical_uri"] != fix.inTree {
			t.Fatalf("golden canonical_uri %v is not the in-tree form %q", payload["canonical_uri"], fix.inTree)
		}
		payload["canonical_uri"] = fix.expected
		fact.Payload = payload
	}
	return fact
}

// TestParityExercisesTheWebUIDifference guards the parity exception: the
// recorded space-basic scenario has three root-relative pages under a /wiki
// base, so each must be rewritten. Without it a change that emptied the
// exception would pass the parity test unnoticed.
func TestParityExercisesTheWebUIDifference(t *testing.T) {
	t.Parallel()

	scenario, _ := loadScenario(t, "space-basic")
	fixes := webUIFixes(t, scenario)
	if len(fixes) != 3 {
		t.Fatalf("space-basic has %d pages with a rewritten web UI link, want 3: %v", len(fixes), fixes)
	}
	for id, fix := range fixes {
		if !strings.Contains(fix.expected, "/wiki/spaces/") || strings.Contains(fix.inTree, "/wiki/") {
			t.Errorf("page %s: expected %q and in-tree %q do not differ by the /wiki path", id, fix.expected, fix.inTree)
		}
	}
}
