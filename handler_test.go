// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

func requestFor(site *fakeSite) Request {
	return Request{
		ProtocolVersion: sdk.ProtocolVersionV1Alpha1,
		Claim:           testClaim(),
		Contract:        Contract(),
		Config: map[string]any{"source": map[string]any{
			"base_url": site.baseURL(),
			"space_id": site.space.ID,
		}},
	}
}

func TestHandleServesAClaimEndToEnd(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	handled, err := Handle(t.Context(), requestFor(site), HandleOptions{Getenv: envFrom(basicEnv), Clock: fixedClock()})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if handled.Outcome.State != sdk.ResultComplete {
		t.Fatalf("State = %q, want complete", handled.Outcome.State)
	}
	assertHostAccepts(t, handled.Outcome.Result)
	if handled.Requests["get_space/success"] != 1 || handled.Requests["fetch_page/success"] != 2 {
		t.Errorf("requests = %v, want one space read and two page reads", handled.Requests)
	}
	line, err := HealthLine(handled, 1500*time.Millisecond)
	if err != nil {
		t.Fatalf("HealthLine() error = %v", err)
	}
	var health struct {
		Health Health `json:"health"`
	}
	if err := json.Unmarshal(line, &health); err != nil {
		t.Fatalf("Unmarshal(health) error = %v", err)
	}
	if health.Health.State != sdk.ResultComplete || health.Health.FactCount != len(handled.Outcome.Facts) || health.Health.ElapsedMS != 1500 {
		t.Errorf("health = %+v", health.Health)
	}
	for _, leak := range []string{canaryToken, site.server.URL, "Runbook"} {
		if strings.Contains(string(line), leak) {
			t.Errorf("health line leaks %q: %s", leak, line)
		}
	}
}

func TestHandleReturnsATerminalResultForABadActivationConfig(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	request := requestFor(site)
	request.Config["source"].(map[string]any)["space_ids"] = "1,2"
	handled, err := Handle(t.Context(), request, HandleOptions{Getenv: envFrom(basicEnv), Clock: fixedClock()})
	if err != nil {
		t.Fatalf("Handle() error = %v, want a result the host records", err)
	}
	if handled.Outcome.State != sdk.ResultTerminal || failureClassOf(t, handled.Outcome.Result) != FailureInvalidConfig {
		t.Fatalf("state %q class %q, want terminal invalid-config", handled.Outcome.State, failureClassOf(t, handled.Outcome.Result))
	}
	if handled.Outcome.Claim != request.Claim {
		t.Error("the failure result must echo the host claim")
	}
	if len(site.recorded()) != 0 {
		t.Error("a bad config must not reach the provider")
	}
	assertHostAccepts(t, handled.Outcome.Result)
}

func TestHandleReportsMissingCredentialsWithoutLeaking(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	handled, err := Handle(t.Context(), requestFor(site), HandleOptions{Getenv: envFrom(map[string]string{}), Clock: fixedClock()})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if failureClassOf(t, handled.Outcome.Result) != FailureMissingCredentials {
		t.Fatalf("class = %q, want missing-credentials", failureClassOf(t, handled.Outcome.Result))
	}
	if !strings.Contains(handled.Detail, "ESHU_CONFLUENCE_API_TOKEN") {
		t.Errorf("detail = %q, want it to name the variable to set", handled.Detail)
	}
}

func TestHandleRefusesWhatItCannotAnswer(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	tests := map[string]func(*Request){
		"protocol":      func(r *Request) { r.ProtocolVersion = "collector-sdk/v9" },
		"generation":    func(r *Request) { r.Claim.GenerationID = "" },
		"scope":         func(r *Request) { r.Claim.Scope.ID = " " },
		"fencing":       func(r *Request) { r.Claim.FencingToken = "" },
		"attempt":       func(r *Request) { r.Claim.Attempt = 0 },
		"deadline":      func(r *Request) { r.Claim.Deadline = time.Time{} },
		"config handle": func(r *Request) { r.Claim.ConfigHandle = "" },
	}
	for name, mutate := range tests {
		request := requestFor(site)
		mutate(&request)
		if _, err := Handle(t.Context(), request, HandleOptions{Getenv: envFrom(basicEnv)}); err == nil {
			t.Errorf("%s: Handle() error = nil, want a refusal", name)
		}
	}
}

func TestDecodeRequestBoundsAndValidatesInput(t *testing.T) {
	t.Parallel()

	good := `{"protocol_version":"collector-sdk/v1alpha1","claim":{"component_id":"x"},"contract":{"protocol_version":"collector-sdk/v1alpha1","facts":[]},"config":{"source":{}},"future_field":1}`
	if _, err := DecodeRequest(strings.NewReader(good)); err != nil {
		t.Errorf("DecodeRequest(good) error = %v, want unknown host fields tolerated", err)
	}
	for name, input := range map[string]string{
		"empty":    "",
		"garbage":  "not json",
		"trailing": good + good,
		"oversize": `{"protocol_version":"` + strings.Repeat("x", maxRequestBytes) + `"}`,
	} {
		if _, err := DecodeRequest(strings.NewReader(input)); err == nil {
			t.Errorf("%s: DecodeRequest() error = nil, want a refusal", name)
		}
	}
}

func TestNoOutputEverCarriesTheCredential(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	site.denied["1002"] = 403
	overrideStatus(site, "/wiki/api/v2/pages/1001", 500, nil)
	handled, err := Handle(t.Context(), requestFor(site), HandleOptions{Getenv: envFrom(basicEnv), Clock: fixedClock()})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	line, _ := HealthLine(handled, time.Second)
	raw, _ := json.Marshal(handled.Outcome)
	for _, output := range []string{string(raw), string(line), handled.Detail} {
		if strings.Contains(output, canaryToken) || strings.Contains(output, "bot@example.invalid") {
			t.Errorf("output leaks a credential: %s", output)
		}
	}
}
