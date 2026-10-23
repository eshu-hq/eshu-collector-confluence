// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

// The parity test replays the recorded Confluence HTTP fixtures in
// testdata/source through this collector and compares the facts with the
// golden output the in-tree collector produced for the same fixtures
// (testdata/golden, see testdata/README.md). Parity is judged on fact kind,
// stable key, payload, source reference, scope, and generation semantics. It
// is not judged on FactID, which the host builds.

type recordedExchange struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   string            `json:"query"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

type recordedScenario struct {
	Description string `json:"description"`
	Mode        string `json:"mode"`
	Config      struct {
		BaseURL             string `json:"base_url"`
		SpaceID             string `json:"space_id"`
		RootPageID          string `json:"root_page_id"`
		SpaceKey            string `json:"space_key"`
		PageLimit           int    `json:"page_limit"`
		MaxTotalPages       int    `json:"max_total_pages"`
		AllowEmptySuccessor bool   `json:"allow_empty_successor"`
	} `json:"config"`
	Exchanges []recordedExchange `json:"exchanges"`
}

type goldenFact struct {
	Kind             string         `json:"kind"`
	SchemaVersion    string         `json:"schema_version"`
	StableKey        string         `json:"stable_key"`
	SourceConfidence string         `json:"source_confidence"`
	CollectorKind    string         `json:"collector_kind"`
	Payload          map[string]any `json:"payload"`
	SourceRef        struct {
		SourceSystem   string `json:"source_system"`
		ScopeID        string `json:"scope_id"`
		FactKey        string `json:"fact_key"`
		SourceURI      string `json:"source_uri"`
		SourceRecordID string `json:"source_record_id"`
	} `json:"source_ref"`
}

type golden struct {
	Scenario          string `json:"scenario"`
	Outcome           string `json:"outcome"`
	Error             string `json:"error"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
	Scope             struct {
		ID            string `json:"id"`
		SourceSystem  string `json:"source_system"`
		ScopeKind     string `json:"scope_kind"`
		CollectorKind string `json:"collector_kind"`
	} `json:"scope"`
	Generation struct {
		TrustedDeletion bool   `json:"trusted_deletion"`
		TriggerKind     string `json:"trigger_kind"`
	} `json:"generation"`
	Facts    []goldenFact `json:"facts"`
	Requests []string     `json:"requests"`
}

// fixtureTransport answers requests from the recorded exchanges and records
// which ones were made. A request with no recorded exchange gets a 599 and is
// reported, so request-shape drift fails the test.
type fixtureTransport struct {
	mu        sync.Mutex
	exchanges map[string]recordedExchange
	used      map[string]struct{}
	missed    []string
}

func (f *fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.Method + " " + req.URL.Path + "?" + req.URL.RawQuery
	f.mu.Lock()
	defer f.mu.Unlock()
	exchange, ok := f.exchanges[key]
	if !ok {
		f.missed = append(f.missed, key)
		return &http.Response{StatusCode: 599, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("no recorded exchange")), Request: req}, nil
	}
	f.used[key] = struct{}{}
	header := http.Header{"Content-Type": {"application/json"}}
	for name, value := range exchange.Headers {
		header.Set(name, value)
	}
	body := []byte(exchange.Body)
	if len(body) == 0 {
		body = []byte("{}")
	}
	return &http.Response{StatusCode: exchange.Status, Header: header, Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
}

func loadScenario(t *testing.T, name string) (recordedScenario, *fixtureTransport) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "source", name+".json"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var scenario recordedScenario
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	transport := &fixtureTransport{exchanges: map[string]recordedExchange{}, used: map[string]struct{}{}}
	for _, exchange := range scenario.Exchanges {
		transport.exchanges[exchange.Method+" "+exchange.Path+"?"+exchange.Query] = exchange
	}
	return scenario, transport
}

func loadGolden(t *testing.T, name string) golden {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "golden", name+".json"))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	var out golden
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode golden %s: %v", name, err)
	}
	return out
}

func scenarioNames(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "source", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	names := make([]string, 0, len(paths))
	for _, path := range paths {
		names = append(names, strings.TrimSuffix(filepath.Base(path), ".json"))
	}
	sort.Strings(names)
	return names
}

func (s recordedScenario) config(concurrency int) Config {
	return Config{
		BaseURL:             s.Config.BaseURL,
		SpaceID:             s.Config.SpaceID,
		RootPageID:          s.Config.RootPageID,
		SpaceKey:            s.Config.SpaceKey,
		PageLimit:           s.Config.PageLimit,
		MaxTotalPages:       s.Config.MaxTotalPages,
		AllowEmptySuccessor: s.Config.AllowEmptySuccessor,
		Credentials:         Credentials{Email: "bot@example.invalid", APIToken: canaryToken},
		ClaimTimeout:        time.Minute,
		FetchConcurrency:    concurrency,
		MaxPageBytes:        defaultMaxPageBytes,
		MaxResultBytes:      defaultMaxResultBytes,
		MaxResponseBytes:    defaultMaxResponseBytes,
	}
}

func TestEveryFixtureHasAGoldenAndNothingElse(t *testing.T) {
	t.Parallel()

	sources := scenarioNames(t)
	goldens, _ := filepath.Glob(filepath.Join("testdata", "golden", "*.json"))
	var goldenNames []string
	for _, path := range goldens {
		goldenNames = append(goldenNames, strings.TrimSuffix(filepath.Base(path), ".json"))
	}
	sort.Strings(goldenNames)
	if !reflect.DeepEqual(sources, goldenNames) {
		t.Fatalf("fixtures %v and goldens %v must pair one to one", sources, goldenNames)
	}
}

func TestParityWithTheInTreeCollector(t *testing.T) {
	t.Parallel()

	for _, name := range scenarioNames(t) {
		for _, concurrency := range []int{1, 8} {
			t.Run(fmt.Sprintf("%s/workers=%d", name, concurrency), func(t *testing.T) {
				t.Parallel()
				scenario, transport := loadScenario(t, name)
				want := loadGolden(t, name)
				claim := testClaim()
				if want.Scope.ID != "" {
					claim.Scope.ID = want.Scope.ID
				}
				collector := &Collector{
					Config: scenario.config(concurrency),
					HTTP:   &http.Client{Transport: transport},
					Clock:  fixedClock(),
				}
				got := collector.Collect(t.Context(), claim)

				if len(transport.missed) > 0 {
					t.Fatalf("requests with no recorded exchange (request shape drifted from the in-tree collector): %v", transport.missed)
				}
				switch want.Outcome {
				case "collected":
					assertCollectedParity(t, got, want, claim)
					assertSameRequests(t, transport, want)
				case "retry_scheduled":
					assertRetryParity(t, got, want)
				case "error":
					assertErrorParity(t, name, got, want)
				default:
					t.Fatalf("unknown golden outcome %q", want.Outcome)
				}
			})
		}
	}
}

func assertCollectedParity(t *testing.T, got Outcome, want golden, claim sdk.Claim) {
	t.Helper()
	assertHostAccepts(t, got.Result)

	wantPartial := false
	for _, fact := range want.Facts {
		if fact.Kind != FactKindSource {
			continue
		}
		metadata, _ := fact.Payload["source_metadata"].(map[string]any)
		wantPartial = metadata["sync_status"] == "partial" || metadata["coverage_warning"] == "truncated"
	}
	wantState := sdk.ResultComplete
	if wantPartial {
		wantState = sdk.ResultPartial
	}
	if got.State != wantState {
		t.Errorf("State = %q, want %q (golden sync_status/coverage_warning)", got.State, wantState)
	}

	// Scope and generation semantics.
	if want.Scope.SourceSystem != claim.SourceSystem || want.Scope.CollectorKind != CollectorKind || want.Scope.ScopeKind != claim.Scope.Kind {
		t.Errorf("golden scope %+v disagrees with the claim identity %q/%q/%q", want.Scope, claim.SourceSystem, CollectorKind, claim.Scope.Kind)
	}
	if got.Claim.Scope.ID != want.Scope.ID {
		t.Errorf("scope id = %q, want %q", got.Claim.Scope.ID, want.Scope.ID)
	}
	if got.Generation.ID != claim.GenerationID {
		t.Errorf("generation id = %q, want the host claim generation (the in-tree id is content-addressed; see testdata/README.md)", got.Generation.ID)
	}
	if got.TrustedDeletion != want.Generation.TrustedDeletion {
		t.Errorf("TrustedDeletion = %v, want %v", got.TrustedDeletion, want.Generation.TrustedDeletion)
	}
	if want.Generation.TriggerKind != "snapshot" {
		t.Errorf("golden trigger kind = %q, want snapshot", want.Generation.TriggerKind)
	}

	// Facts: same set by kind and stable key, same everything else.
	gotFacts := map[string]sdk.Fact{}
	for _, fact := range got.Facts {
		key := fact.Kind + "|" + fact.StableKey
		if _, dup := gotFacts[key]; dup {
			t.Errorf("duplicate fact %s", key)
		}
		gotFacts[key] = fact
	}
	wantKeys := map[string]bool{}
	for _, wantFact := range want.Facts {
		key := wantFact.Kind + "|" + wantFact.StableKey
		wantKeys[key] = true
		fact, ok := gotFacts[key]
		if !ok {
			t.Errorf("missing fact %s (payload %v)", key, wantFact.Payload)
			continue
		}
		if fact.SchemaVersion != wantFact.SchemaVersion {
			t.Errorf("%s schema_version = %q, want %q", key, fact.SchemaVersion, wantFact.SchemaVersion)
		}
		if string(fact.SourceConfidence) != wantFact.SourceConfidence {
			t.Errorf("%s source_confidence = %q, want %q", key, fact.SourceConfidence, wantFact.SourceConfidence)
		}
		if wantFact.CollectorKind != CollectorKind {
			t.Errorf("%s golden collector_kind = %q, want %q", key, wantFact.CollectorKind, CollectorKind)
		}
		if !reflect.DeepEqual(fact.Payload, wantFact.Payload) {
			t.Errorf("%s payload differs:\n got %s\nwant %s", key, mustJSON(fact.Payload), mustJSON(wantFact.Payload))
		}
		ref := fact.SourceRef
		wantRef := wantFact.SourceRef
		if ref.SourceSystem != wantRef.SourceSystem || ref.ScopeID != wantRef.ScopeID || ref.FactKey != wantRef.FactKey ||
			ref.URI != wantRef.SourceURI || ref.RecordID != wantRef.SourceRecordID {
			t.Errorf("%s source_ref = %+v, want %+v", key, ref, wantRef)
		}
	}
	for key := range gotFacts {
		if !wantKeys[key] {
			t.Errorf("extra fact %s not emitted by the in-tree collector", key)
		}
	}
}

func assertSameRequests(t *testing.T, transport *fixtureTransport, want golden) {
	t.Helper()
	var got []string
	for key := range transport.used {
		got = append(got, key)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want.Requests) {
		t.Errorf("provider requests differ from the in-tree collector:\n got %v\nwant %v", got, want.Requests)
	}
}

func assertRetryParity(t *testing.T, got Outcome, want golden) {
	t.Helper()
	if got.State != sdk.ResultRetryable {
		t.Fatalf("State = %q, want retryable where the in-tree collector schedules a retry", got.State)
	}
	assertHostAccepts(t, got.Result)
	seconds := got.Statuses[0].RetryAfterSeconds
	if want.RetryAfterSeconds >= 5 {
		// A provider Retry-After is honored exactly.
		if seconds != want.RetryAfterSeconds {
			t.Errorf("RetryAfterSeconds = %d, want the provider's %d", seconds, want.RetryAfterSeconds)
		}
		return
	}
	// Without provider guidance both use a one second base plus up to a
	// quarter second of deterministic jitter. The jitter key differs (the claim
	// scope here), and this collector rounds up to whole seconds.
	if seconds < want.RetryAfterSeconds || seconds > want.RetryAfterSeconds+1 {
		t.Errorf("RetryAfterSeconds = %d, want %d or %d", seconds, want.RetryAfterSeconds, want.RetryAfterSeconds+1)
	}
}

// errorMapping is the documented translation of the in-tree failure to this
// collector's result. server-500 is an intentional difference: the in-tree
// collector exits and restarts on a 500, which is a retry in effect, so this
// collector asks the host for a bounded retry instead of dead-lettering.
var errorMapping = map[string]struct {
	state sdk.ResultState
	class string
}{
	"error-forbidden-space": {sdk.ResultTerminal, FailurePermissionDenied},
	"error-unauthorized":    {sdk.ResultTerminal, FailureAuthFailed},
	"error-server-500":      {sdk.ResultRetryable, FailureProviderUnavailable},
}

func assertErrorParity(t *testing.T, name string, got Outcome, want golden) {
	t.Helper()
	expected, ok := errorMapping[name]
	if !ok {
		t.Fatalf("scenario %s has no documented error mapping (in-tree error %q)", name, want.Error)
	}
	if got.State != expected.state || failureClassOf(t, got.Result) != expected.class {
		t.Errorf("state %q class %q, want %q %q", got.State, failureClassOf(t, got.Result), expected.state, expected.class)
	}
	assertHostAccepts(t, got.Result)
}

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return err.Error()
	}
	return string(raw)
}
