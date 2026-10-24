// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

// conformanceScenarios maps each committed SDK result fixture to the recorded
// Confluence scenario that produces it. The fixtures feed `eshu component
// conform` and the in-process conformance tests.
var conformanceScenarios = map[string]string{
	"complete":  "space-basic",
	"partial":   "space-permission-denied",
	"truncated": "space-truncated",
	"empty":     "space-empty",
	"retryable": "error-rate-limited",
	"terminal":  "error-unauthorized",
}

const conformanceDir = "testdata/conformance"

// buildConformanceResult replays a recorded scenario and returns the wire JSON
// of its result.
func buildConformanceResult(t *testing.T, scenarioName string) []byte {
	t.Helper()
	scenario, transport := loadScenario(t, scenarioName)
	want := loadGolden(t, scenarioName)
	claim := testClaim()
	if want.Scope.ID != "" {
		claim.Scope.ID = want.Scope.ID
	}
	collector := &Collector{Config: scenario.config(1), HTTP: &http.Client{Transport: transport}, Clock: fixedClock()}
	outcome := collector.Collect(t.Context(), claim)
	raw, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", scenarioName, err)
	}
	return append(raw, '\n')
}

// TestConformanceFixturesAreCurrent fails when a committed fixture no longer
// matches what the collector produces. Set UPDATE_CONFORMANCE_FIXTURES=1 to
// rewrite them after an intended change.
func TestConformanceFixturesAreCurrent(t *testing.T) {
	update := os.Getenv("UPDATE_CONFORMANCE_FIXTURES") == "1"
	for name, scenario := range conformanceScenarios {
		path := filepath.Join(conformanceDir, name+".json")
		built := buildConformanceResult(t, scenario)
		if update {
			if err := os.MkdirAll(conformanceDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, built, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		committed, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v (run with UPDATE_CONFORMANCE_FIXTURES=1)", path, err)
		}
		if !bytes.Equal(committed, built) {
			t.Errorf("%s is stale; rerun with UPDATE_CONFORMANCE_FIXTURES=1", path)
		}
	}
}

// loadConformanceFixture decodes one committed fixture as an SDK result.
func loadConformanceFixture(t *testing.T, name string) sdk.Result {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(conformanceDir, name+".json"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var result sdk.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return result
}
