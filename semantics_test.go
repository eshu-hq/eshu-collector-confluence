// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

func TestCollectDuplicateDeliveryIsByteIdentical(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	cfg := testConfig(site)
	first, err := json.Marshal(collectOnce(t, site, cfg, testClaim()))
	if err != nil {
		t.Fatalf("Marshal(first) error = %v", err)
	}
	for i := 0; i < 3; i++ {
		again, err := json.Marshal(collectOnce(t, site, cfg, testClaim()))
		if err != nil {
			t.Fatalf("Marshal(again) error = %v", err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("redelivery %d produced a different result:\n%s\n---\n%s", i, first, again)
		}
	}
}

func TestCollectIsDeterministicAcrossFetchConcurrency(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	for _, id := range []string{"6003", "6001", "6002", "6005", "6004"} {
		site.addPage(samplePage(id, "Page "+id))
	}
	cfg := testConfig(site)
	cfg.FetchConcurrency = 1
	serial, _ := json.Marshal(collectOnce(t, site, cfg, testClaim()))
	cfg.FetchConcurrency = 8
	parallel, _ := json.Marshal(collectOnce(t, site, cfg, testClaim()))
	if !bytes.Equal(serial, parallel) {
		t.Fatalf("worker count changed the result:\n%s\n---\n%s", serial, parallel)
	}
}

func TestFreshnessHintTracksContentNotTime(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	cfg := testConfig(site)
	base := collectOnce(t, site, cfg, testClaim()).Generation.FreshnessHint

	later := testClaim()
	later.GenerationID, later.SourceRunID, later.WorkItemID = "component-generation:next", "component-generation:next", "component-work:next"
	if got := collectOnce(t, site, cfg, later).Generation.FreshnessHint; got != base {
		t.Errorf("hint changed with only the generation id: %q != %q", got, base)
	}

	changes := map[string]func(){
		"page version": func() {
			page := site.pages["1001"]
			page.Version.Number = 2
			site.pages["1001"] = page
		},
		"page body": func() {
			page := site.pages["1001"]
			page.Body.Storage.Value += "<p>more</p>"
			site.pages["1001"] = page
		},
		"page label": func() {
			page := site.pages["1001"]
			page.Labels = labelResults{Results: []Label{{Name: "runbook"}, {Name: "new"}}}
			site.pages["1001"] = page
		},
		"page removed": func() {
			delete(site.pages, "1002")
			site.order = []string{"1001"}
		},
	}
	previous := base
	for name, change := range changes {
		site.mu.Lock()
		change()
		site.mu.Unlock()
		hint := collectOnce(t, site, cfg, testClaim()).Generation.FreshnessHint
		if hint == previous {
			t.Errorf("%s did not change the freshness hint", name)
		}
		previous = hint
	}
}

// TestEmptySuccessorAttestationFailsClosed proves the one in-tree option the
// published SDK cannot carry is refused, not ignored: attestation.allow_empty_successor
// true is a terminal unsupported_option before any provider request, while
// absent and false behave the same as each other.
func TestEmptySuccessorAttestationFailsClosed(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	request := requestFor(site)
	request.Config["attestation"] = map[string]any{"allow_empty_successor": true}
	handled, err := Handle(t.Context(), request, HandleOptions{Getenv: envFrom(basicEnv), Clock: fixedClock()})
	if err != nil {
		t.Fatalf("Handle() error = %v, want a result the host records", err)
	}
	result := handled.Result
	if result.State != sdk.ResultTerminal || failureClassOf(t, result) != FailureUnsupportedOption {
		t.Fatalf("state %q class %q, want terminal unsupported_option", result.State, failureClassOf(t, result))
	}
	if len(result.Facts) != 0 || len(site.recorded()) != 0 {
		t.Errorf("facts = %d, provider requests = %d, want none before the refusal", len(result.Facts), len(site.recorded()))
	}
	if result.Claim != request.Claim || result.Generation.ID != request.Claim.GenerationID {
		t.Error("the refusal must echo the host claim and generation")
	}
	assertHostAccepts(t, result)
	if !strings.Contains(handled.Detail, "attestation.allow_empty_successor") || !strings.Contains(handled.Detail, "SDK") {
		t.Errorf("detail = %q, want it to name the option and the SDK limitation", handled.Detail)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "trusted_deletion") {
		t.Errorf("result carries an unpublished wire field: %s", raw)
	}
}

func TestEmptySuccessorAttestationAbsentAndFalseAreEquivalent(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	run := func(attestation any) []byte {
		request := requestFor(site)
		if attestation != nil {
			request.Config["attestation"] = attestation
		}
		handled, err := Handle(t.Context(), request, HandleOptions{Getenv: envFrom(basicEnv), Clock: fixedClock()})
		if err != nil {
			t.Fatalf("Handle() error = %v", err)
		}
		if handled.Result.State != sdk.ResultComplete {
			t.Fatalf("State = %q, want complete", handled.Result.State)
		}
		raw, _ := json.Marshal(handled.Result)
		return raw
	}
	absent := run(nil)
	explicitFalse := run(map[string]any{"allow_empty_successor": false})
	if !bytes.Equal(absent, explicitFalse) {
		t.Errorf("absent and false differ:\n%s\n---\n%s", absent, explicitFalse)
	}
	if strings.Contains(string(absent), "trusted_deletion") {
		t.Errorf("an empty snapshot carries trusted_deletion: %s", absent)
	}
}

func TestCollectNeverEmitsUnchanged(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	cfg := testConfig(site)
	for i := 0; i < 2; i++ {
		if got := collectOnce(t, site, cfg, testClaim()).State; got == sdk.ResultUnchanged {
			t.Fatalf("run %d emitted unchanged; the host supplies no previous snapshot identity", i)
		}
	}
}
