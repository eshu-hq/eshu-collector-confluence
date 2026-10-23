# Parity fixtures

`source/` holds recorded Confluence REST v2 exchanges. `golden/` holds what the
in-tree Eshu Confluence collector produced for each of them. `parity_test.go`
replays every fixture through this collector and compares the result with the
golden. All content is synthetic: the host is the reserved `.invalid` TLD, and
no real tenant data, hostname, user, or token appears.

## Fixture format (`source/<scenario>.json`)

```json
{
  "description": "what the scenario covers",
  "mode": "space | page_tree",
  "config": {"base_url": "...", "space_id": "...", "page_limit": 2, "max_total_pages": 5000},
  "exchanges": [
    {"method": "GET", "path": "/wiki/api/v2/spaces/12345", "query": "", "status": 200,
     "headers": {"Retry-After": "17"}, "body": {}}
  ]
}
```

An exchange matches on method, path, and the exact raw query string. A request
with no matching exchange gets HTTP 599 and fails the test, so the fixtures also
prove both collectors send the same requests. The golden records the set of
requests the in-tree collector made, and the test compares it with this
collector's set.

## Golden format (`golden/<scenario>.json`)

`outcome` is `collected`, `retry_scheduled`, or `error`. A collected golden
lists scope (`id`, `source_system`, `scope_kind`, `collector_kind`), generation
(`freshness_hint`, `trusted_deletion`, `trigger_kind`, `id`), and every fact as
`kind`, `schema_version`, `stable_key`, `source_confidence`, `payload`, and
`source_ref`, sorted by kind and stable key. FactID and observed_at are left
out: the host builds the FactID, and the observation time is a clock reading.

Goldens were produced from `eshu-hq/eshu` commit `ce050339cf` (`origin/main` on
2026-10-23). The in-tree Confluence code at that commit is identical to the
code at `c697b07618`.

## Reproduce the goldens

The generator is a throwaway test inside the in-tree package. It is not
committed anywhere; its source is the block below.

```bash
git clone https://github.com/eshu-hq/eshu eshu-golden && cd eshu-golden
git checkout ce050339cf
# Save the block below as go/internal/collector/confluence/zz_golden_confluence_test.go
cd go
GOLDEN_FIXTURES=/abs/path/to/eshu-collector-confluence/testdata/source \
GOLDEN_OUT=/abs/path/to/eshu-collector-confluence/testdata/golden \
  go test ./internal/collector/confluence -run TestZZGoldenConfluence -count=1
git -C /abs/path/to/eshu-collector-confluence diff --stat testdata/golden   # expect no change
rm internal/collector/confluence/zz_golden_confluence_test.go
```

The fixtures themselves come from a throwaway script that writes the JSON; edit
the JSON by hand to add a scenario, then regenerate the goldens and rerun
`go test ./...` here.

## Intentional differences

The parity test asserts everything below as an explicit mapping, so a change in
any of it fails the test.

| Area | In-tree collector | This collector | Why |
| --- | --- | --- | --- |
| `FactID` | `ConfluenceDocumentationFact` stable id | host-built envelope id | The host builds the envelope; parity is judged on kind, stable key, payload, scope, generation. |
| Generation id | content-addressed from page versions | the host claim's generation id | The host mints one generation per claim and rejects any other id. |
| Freshness hint | latest page `createdAt` | digest of every emitted kind, stable key, and payload, plus the extractor version | The core store skips a snapshot whose hint equals the active generation's, so the hint must change with any emitted change; the latest timestamp misses deletions, label edits, and extractor changes. |
| Empty-successor attestation | `ESHU_CONFLUENCE_ALLOW_EMPTY_SUCCESSOR` sets `trusted_deletion` on the generation | `attestation.allow_empty_successor: true` is refused, terminal `unsupported_option` | SDK v0.2.0 has no `Generation.TrustedDeletion` and this collector does not hand-marshal unpublished wire fields. The `space-empty-attested` golden records the in-tree attestation; its test asserts the refusal. |
| HTTP 500, 502, 504 | fatal error (process exits and restarts) | retryable result | A restart is a retry in effect. The host retries with backoff and dead-letters the work item at its attempt budget (10 in `collector-component-extension`), keeping the class. 429 and 503 behave as before. |
| Retry timing | in-process backoff state | backoff computed from the claim attempt | Each claim is a fresh process. The jitter key is the claim scope, so a delay can differ by up to one second. |
| Consecutive transport failures | fatal after 20 in a row | retryable `transport-error` at every attempt | The extension host owns the attempt budget: it ends a retrying claim at attempt 10 and keeps the class of the last failure, so a limit of 20 here could never fire. `claim.Attempt` also counts non-transport attempts, so it is not a count of transport failures. |
| Pagination links | an absolute `_links.next` is followed to any host | followed only on the configured origin | The collector sends credentials with every request. |
| Provider text | stored as received | NUL removed, invalid UTF-8 replaced | A NUL byte makes the Postgres commit fail forever. |
| Hostile page links | a credential in a link fails the commit | replaced by the REST URI | The SDK validator rejects the whole result otherwise. |
| Bounds | none on response, page, or result size | `max_response_bytes`, `max_page_bytes`, `max_result_bytes` | The host reads at most 4 MiB of stdout by default. A page over a bound is skipped whole and the snapshot is `partial`; a response over its bound fails the claim as terminal. |
| Page detail fetches | sequential | up to `fetch_concurrency` in flight | Throughput inside one claim lease; the output order does not depend on timing. |
| Multi-space mode | `ESHU_CONFLUENCE_SPACE_IDS` | not supported | One activation per scope. |
| Truth extraction | optional doctruth extractor | not ported | The in-tree command never wires it. |

## The generator

```go
package confluence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type goldenExchange struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   string            `json:"query"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

type goldenFixture struct {
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
	Exchanges []goldenExchange `json:"exchanges"`
}

// replay serves the recorded exchanges and records which requests were made.
type replay struct {
	mu        sync.Mutex
	exchanges map[string]goldenExchange
	used      map[string]struct{}
	missed    []string
}

func (r *replay) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.Method + " " + req.URL.Path + "?" + req.URL.RawQuery
	r.mu.Lock()
	defer r.mu.Unlock()
	exchange, ok := r.exchanges[key]
	if !ok {
		r.missed = append(r.missed, key)
		return &http.Response{StatusCode: 599, Body: io.NopCloser(strings.NewReader("no recorded exchange")), Header: http.Header{}, Request: req}, nil
	}
	r.used[key] = struct{}{}
	header := http.Header{"Content-Type": {"application/json"}}
	for k, v := range exchange.Headers {
		header.Set(k, v)
	}
	body := []byte(exchange.Body)
	if len(body) == 0 {
		body = []byte("{}")
	}
	return &http.Response{StatusCode: exchange.Status, Header: header, Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
}

func TestZZGoldenConfluence(t *testing.T) {
	fixtures, out := os.Getenv("GOLDEN_FIXTURES"), os.Getenv("GOLDEN_OUT")
	if fixtures == "" || out == "" {
		t.Skip("GOLDEN_FIXTURES and GOLDEN_OUT are not set")
	}
	paths, _ := filepath.Glob(filepath.Join(fixtures, "*.json"))
	sort.Strings(paths)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fixture goldenFixture
		if err := json.Unmarshal(raw, &fixture); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		transport := &replay{exchanges: map[string]goldenExchange{}, used: map[string]struct{}{}}
		for _, exchange := range fixture.Exchanges {
			transport.exchanges[exchange.Method+" "+exchange.Path+"?"+exchange.Query] = exchange
		}
		client, err := NewHTTPClient(HTTPClientConfig{
			BaseURL: fixture.Config.BaseURL, Email: "bot@example.invalid", APIToken: "synthetic-token",
			Client: &http.Client{Transport: transport},
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		source := &Source{Client: client, Config: SourceConfig{
			BaseURL: fixture.Config.BaseURL, SpaceID: fixture.Config.SpaceID, RootPageID: fixture.Config.RootPageID,
			SpaceKey: fixture.Config.SpaceKey, PageLimit: fixture.Config.PageLimit, MaxTotalPages: fixture.Config.MaxTotalPages,
			AllowEmptySuccessor: fixture.Config.AllowEmptySuccessor, Now: func() time.Time { return now },
		}}
		collected, ok, nextErr := source.Next(context.Background())
		golden := map[string]any{"scenario": name, "source": "go/internal/collector/confluence (Source.Next over the recorded fixture)"}
		switch {
		case nextErr != nil:
			golden["outcome"] = "error"
			golden["error"] = nextErr.Error()
		case !ok:
			golden["outcome"] = "retry_scheduled"
			golden["retry_after_seconds"] = int(source.retry.nextAttemptAt.Sub(now) / time.Second)
		default:
			golden["outcome"] = "collected"
			golden["scope"] = map[string]any{
				"id": collected.Scope.ScopeID, "source_system": collected.Scope.SourceSystem,
				"scope_kind": string(collected.Scope.ScopeKind), "collector_kind": string(collected.Scope.CollectorKind),
			}
			golden["generation"] = map[string]any{
				"freshness_hint": collected.Generation.FreshnessHint, "trusted_deletion": collected.Generation.TrustedDeletion,
				"trigger_kind": string(collected.Generation.TriggerKind), "id": collected.Generation.GenerationID,
			}
			var facts []map[string]any
			for envelope := range collected.Facts {
				facts = append(facts, map[string]any{
					"kind": envelope.FactKind, "schema_version": envelope.SchemaVersion, "stable_key": envelope.StableFactKey,
					"source_confidence": string(envelope.SourceConfidence), "collector_kind": envelope.CollectorKind,
					"payload": envelope.Payload,
					"source_ref": map[string]any{
						"source_system": envelope.SourceRef.SourceSystem, "scope_id": envelope.SourceRef.ScopeID,
						"fact_key": envelope.SourceRef.FactKey, "source_uri": envelope.SourceRef.SourceURI,
						"source_record_id": envelope.SourceRef.SourceRecordID,
					},
				})
			}
			sort.Slice(facts, func(i, j int) bool {
				ki, kj := facts[i]["kind"].(string)+"|"+facts[i]["stable_key"].(string), facts[j]["kind"].(string)+"|"+facts[j]["stable_key"].(string)
				return ki < kj
			})
			golden["facts"] = facts
		}
		var requests []string
		for key := range transport.used {
			requests = append(requests, key)
		}
		sort.Strings(requests)
		golden["requests"] = requests
		if len(transport.missed) > 0 {
			t.Fatalf("%s: in-tree collector made requests with no recorded exchange: %v", name, transport.missed)
		}
		encoded, err := json.MarshalIndent(golden, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, name+".json"), append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("golden %s: %s\n", name, golden["outcome"])
	}
}
```
