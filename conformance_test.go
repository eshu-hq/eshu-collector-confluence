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

func payloadSchemas(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	schemas := map[string]json.RawMessage{}
	for _, kind := range []string{FactKindSource, FactKindDocument, FactKindSection, FactKindLink} {
		raw, ok := fixturepack.SchemaFor(kind)
		if !ok {
			t.Fatalf("fixturepack has no schema for %s", kind)
		}
		schemas[kind] = raw
	}
	return schemas
}

func allConformanceFixtures(t *testing.T) []sdk.Result {
	t.Helper()
	var results []sdk.Result
	for _, name := range []string{"complete", "partial", "truncated", "empty", "retryable", "terminal"} {
		results = append(results, loadConformanceFixture(t, name))
	}
	return results
}

// hostChecks runs what the extension host runs on every result before it
// commits facts: the SDK contract validator built from the manifest's declared
// facts, then the manifest's payload schemas. It is the part of conformance
// that applies to a producer of core-owned kinds.
func hostChecks(t *testing.T, manifest conformance.Manifest, result sdk.Result) error {
	t.Helper()
	if _, err := sdk.NewValidator(manifest.Contract()).ValidateResult(result); err != nil {
		return err
	}
	return conformance.ValidatePayloadSchemas(payloadSchemas(t), result)
}

// TestFixturesPassTheHostChecks proves every committed result fixture passes
// the contract validator derived from manifest.yaml and the payload schemas the
// manifest references, using only the published SDK modules.
func TestFixturesPassTheHostChecks(t *testing.T) {
	t.Parallel()

	manifest := loadManifest(t)
	for name, fixture := range map[string]sdk.Result{
		"complete": loadConformanceFixture(t, "complete"), "partial": loadConformanceFixture(t, "partial"),
		"truncated": loadConformanceFixture(t, "truncated"), "empty": loadConformanceFixture(t, "empty"),
		"retryable": loadConformanceFixture(t, "retryable"), "terminal": loadConformanceFixture(t, "terminal"),
	} {
		if err := hostChecks(t, manifest, fixture); err != nil {
			t.Errorf("%s fixture rejected: %v", name, err)
		}
	}
}

// TestPublicHarnessCannotYetJudgeCoreKindManifests pins a known gap, not a
// pass. conformance.Run rejects any manifest whose fact kinds are not
// namespaced, and the published harness has no grant concept, so `eshu
// component conform` fails for this collector even when the component home's
// producer grants admit its core documentation kinds. `inspect` and `verify`
// do pass with those grants. When the harness learns grants this test fails:
// replace it with the harness verdict over the committed fixtures. The
// decision is recorded in README.md.
func TestPublicHarnessCannotYetJudgeCoreKindManifests(t *testing.T) {
	t.Parallel()

	report := conformance.Run(conformance.Request{
		Manifest:       loadManifest(t),
		Fixtures:       allConformanceFixtures(t),
		Mode:           conformance.ModeFixture,
		PayloadSchemas: payloadSchemas(t),
	})
	if report.OK() || len(report.Findings) != 1 || report.Findings[0].Code != conformance.FindingManifestInvalid ||
		!strings.Contains(report.Findings[0].Message, "must be namespaced") {
		t.Fatalf("report = %+v, want a single manifest_invalid finding about namespacing; if the harness now accepts granted core kinds, replace this test with its verdict", report)
	}
}

func TestConformanceFixturesCoverEveryResultState(t *testing.T) {
	t.Parallel()

	states := map[sdk.ResultState]bool{}
	for _, fixture := range allConformanceFixtures(t) {
		states[fixture.State] = true
	}
	for _, want := range []sdk.ResultState{sdk.ResultComplete, sdk.ResultPartial, sdk.ResultRetryable, sdk.ResultTerminal} {
		if !states[want] {
			t.Errorf("no committed fixture for state %q", want)
		}
	}
	if states[sdk.ResultUnchanged] {
		t.Error("a fixture claims unchanged; this collector never emits it")
	}
}

// TestConformanceEmptySpaceIsAnAuthoritativeSnapshot proves an empty space
// emits a complete snapshot with just the source fact, which retires stale
// evidence; the core store's successor guard, not an unchanged result,
// protects against an accidental empty read.
func TestConformanceEmptySpaceIsAnAuthoritativeSnapshot(t *testing.T) {
	t.Parallel()

	empty := loadConformanceFixture(t, "empty")
	if empty.State != sdk.ResultComplete || len(empty.Facts) != 1 || empty.Facts[0].Kind != FactKindSource {
		t.Fatalf("empty fixture = %q with %d facts, want complete with the source fact", empty.State, len(empty.Facts))
	}
	if empty.Generation.FreshnessHint == "" {
		t.Error("empty fixture has no freshness hint")
	}
}

func TestHostChecksRejectBrokenResults(t *testing.T) {
	t.Parallel()

	manifest := loadManifest(t)
	clone := func() sdk.Result {
		raw, _ := json.Marshal(loadConformanceFixture(t, "complete"))
		var out sdk.Result
		_ = json.Unmarshal(raw, &out)
		return out
	}
	tests := map[string]struct {
		mutate func(*sdk.Result)
		want   string
	}{
		"unsupported protocol":           {func(r *sdk.Result) { r.ProtocolVersion = "collector-sdk/v9alpha9" }, "protocol_version"},
		"undeclared kind":                {func(r *sdk.Result) { r.Facts[0].Kind = "documentation_claim_candidate" }, "not declared"},
		"undeclared schema":              {func(r *sdk.Result) { r.Facts[1].SchemaVersion = "2.0.0" }, "not declared"},
		"generation mismatch":            {func(r *sdk.Result) { r.Facts[0].SourceRef.GenerationID = "other" }, "generation"},
		"scope mismatch":                 {func(r *sdk.Result) { r.Facts[0].SourceRef.ScopeID = "other" }, "scope"},
		"credential in uri":              {func(r *sdk.Result) { r.Facts[0].SourceRef.URI = "https://user:pw@example.invalid/x" }, "credentials"},
		"sensitive payload key":          {func(r *sdk.Result) { r.Facts[0].Payload["api_token"] = "x" }, "sensitive"},
		"payload missing required field": {func(r *sdk.Result) { delete(r.Facts[1].Payload, "document_id") }, "document_id"},
		"unchanged with facts":           {func(r *sdk.Result) { r.State = sdk.ResultUnchanged }, "must not include facts"},
	}
	for name, tt := range tests {
		fixture := clone()
		tt.mutate(&fixture)
		err := hostChecks(t, manifest, fixture)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error = %v, want containing %q", name, err, tt.want)
		}
	}
}
