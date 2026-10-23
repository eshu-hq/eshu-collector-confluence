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

func TestEmptySuccessorAttestationReachesTheWire(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	cfg := testConfig(site)

	plain := collectOnce(t, site, cfg, testClaim())
	plainJSON, _ := json.Marshal(plain)
	if strings.Contains(string(plainJSON), "trusted_deletion") {
		t.Errorf("unattested result carries trusted_deletion: %s", plainJSON)
	}
	baseline, _ := json.Marshal(plain.Result)
	if !bytes.Equal(plainJSON, baseline) {
		t.Errorf("an unattested result must be byte-identical to a plain sdk.Result:\n%s\n---\n%s", plainJSON, baseline)
	}

	cfg.AllowEmptySuccessor = true
	attested := collectOnce(t, site, cfg, testClaim())
	if !attested.TrustedDeletion {
		t.Fatal("TrustedDeletion = false, want the operator attestation forwarded")
	}
	raw, _ := json.Marshal(attested)
	var decoded struct {
		Generation struct {
			ID              string `json:"id"`
			FreshnessHint   string `json:"freshness_hint"`
			TrustedDeletion bool   `json:"trusted_deletion"`
		} `json:"generation"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !decoded.Generation.TrustedDeletion || decoded.Generation.ID != testClaim().GenerationID || decoded.Generation.FreshnessHint == "" {
		t.Errorf("wire generation = %+v, want trusted_deletion with id and freshness hint kept", decoded.Generation)
	}
	if strings.Count(string(raw), `"generation":`) != 1 {
		t.Errorf("wire result has duplicate generation objects: %s", raw)
	}
	assertHostAccepts(t, attested.Result)
}

func TestFailureResultsNeverAttestDeletion(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	overrideStatus(site, "/wiki/api/v2/spaces/12345", 503, nil)
	cfg := testConfig(site)
	cfg.AllowEmptySuccessor = true
	outcome := collectOnce(t, site, cfg, testClaim())
	if outcome.TrustedDeletion {
		t.Fatal("a retryable failure must never carry the deletion attestation")
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
