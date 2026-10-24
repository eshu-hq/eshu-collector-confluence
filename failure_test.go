// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

// failureClass returns the failure class of a retryable or terminal result.
func failureClassOf(t *testing.T, result sdk.Result) string {
	t.Helper()
	for _, status := range result.Statuses {
		if status.Class == sdk.StatusFailure {
			return status.FailureClass
		}
	}
	t.Fatalf("result %q carries no failure status: %#v", result.State, result.Statuses)
	return ""
}

func overrideStatus(site *fakeSite, path string, status int, header http.Header) {
	site.mu.Lock()
	defer site.mu.Unlock()
	site.override[path] = func(w http.ResponseWriter, r *http.Request) bool {
		for key, values := range header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		http.Error(w, "provider body with "+canaryToken, status)
		return true
	}
}

func TestCollectStatusMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		header     http.Header
		wantState  sdk.ResultState
		wantClass  string
		wantRetry  int
		wantRetryG func(int) bool
	}{
		{name: "rate limited honors Retry-After", status: 429, header: http.Header{"Retry-After": {"17"}}, wantState: sdk.ResultRetryable, wantClass: FailureRateLimited, wantRetry: 17},
		{name: "service unavailable is retryable", status: 503, wantState: sdk.ResultRetryable, wantClass: FailureProviderUnavailable},
		{name: "bad gateway is retryable", status: 502, wantState: sdk.ResultRetryable, wantClass: FailureProviderUnavailable},
		{name: "gateway timeout is retryable", status: 504, wantState: sdk.ResultRetryable, wantClass: FailureProviderUnavailable},
		{name: "server error is retryable", status: 500, wantState: sdk.ResultRetryable, wantClass: FailureProviderUnavailable},
		{name: "unauthorized is terminal", status: 401, wantState: sdk.ResultTerminal, wantClass: FailureAuthFailed},
		{name: "forbidden space is terminal", status: 403, wantState: sdk.ResultTerminal, wantClass: FailurePermissionDenied},
		{name: "missing space is terminal", status: 404, wantState: sdk.ResultTerminal, wantClass: FailurePermissionDenied},
		{name: "bad request is terminal", status: 400, wantState: sdk.ResultTerminal, wantClass: FailureProviderRejected},
		{name: "teapot is terminal", status: 418, wantState: sdk.ResultTerminal, wantClass: FailureProviderRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			site := twoPageSite(t)
			overrideStatus(site, "/wiki/api/v2/spaces/12345", tt.status, tt.header)
			outcome := collectOnce(t, site, testConfig(site), testClaim())

			if outcome.State != tt.wantState {
				t.Fatalf("State = %q, want %q", outcome.State, tt.wantState)
			}
			if got := failureClassOf(t, outcome); got != tt.wantClass {
				t.Errorf("failure class = %q, want %q", got, tt.wantClass)
			}
			if len(outcome.Facts) != 0 {
				t.Errorf("facts = %d, want none on a %s result", len(outcome.Facts), outcome.State)
			}
			status := outcome.Statuses[0]
			if tt.wantState == sdk.ResultRetryable && status.RetryAfterSeconds < 1 {
				t.Errorf("RetryAfterSeconds = %d, want at least 1", status.RetryAfterSeconds)
			}
			if tt.wantRetry != 0 && status.RetryAfterSeconds != tt.wantRetry {
				t.Errorf("RetryAfterSeconds = %d, want %d from Retry-After", status.RetryAfterSeconds, tt.wantRetry)
			}
			assertHostAccepts(t, outcome)
			assertNoCanary(t, outcome)
		})
	}
}

func TestCollectRetryAfterIsBounded(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	overrideStatus(site, "/wiki/api/v2/spaces/12345", 429, http.Header{"Retry-After": {"999999"}})
	outcome := collectOnce(t, site, testConfig(site), testClaim())
	if got := outcome.Statuses[0].RetryAfterSeconds; got != int(maxRetryAfter/time.Second) {
		t.Fatalf("RetryAfterSeconds = %d, want it capped at %d", got, int(maxRetryAfter/time.Second))
	}
}

func TestCollectBackoffGrowsWithAttemptAndIsDeterministic(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	overrideStatus(site, "/wiki/api/v2/spaces/12345", 503, nil)
	delayFor := func(attempt int) int {
		claim := testClaim()
		claim.Attempt = attempt
		return collectOnce(t, site, testConfig(site), claim).Statuses[0].RetryAfterSeconds
	}
	first, third, tenth := delayFor(1), delayFor(3), delayFor(10)
	if !(first < third && third <= tenth) {
		t.Errorf("delays = %d, %d, %d, want growth with the attempt", first, third, tenth)
	}
	if tenth > 75 {
		t.Errorf("attempt 10 delay = %d, want it capped near one minute plus jitter", tenth)
	}
	if delayFor(3) != third {
		t.Error("the same attempt must produce the same delay")
	}
}

func TestCollectTransportFailureStaysRetryableAtEveryAttempt(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	site.mu.Lock()
	site.override["/wiki/api/v2/spaces/12345"] = func(w http.ResponseWriter, r *http.Request) bool {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("response writer cannot hijack")
			return true
		}
		conn, _, err := hijacker.Hijack()
		if err == nil {
			_ = conn.Close() // closes the connection without a response
		}
		return true
	}
	site.mu.Unlock()

	// The extension host ends a retrying claim at its own attempt budget
	// (10 in collector-component-extension) and keeps the class of the last
	// failure, so the collector must keep answering retryable instead of
	// guessing a limit of its own that the host would never let it reach.
	for _, attempt := range []int{1, 9, 10, 11, 25} {
		claim := testClaim()
		claim.Attempt = attempt
		outcome := collectOnce(t, site, testConfig(site), claim)
		if outcome.State != sdk.ResultRetryable || failureClassOf(t, outcome) != FailureTransport {
			t.Fatalf("attempt %d: state %q class %q, want retryable transport-error", attempt, outcome.State, failureClassOf(t, outcome))
		}
		assertHostAccepts(t, outcome)
	}
}

func TestCollectConnectionRefusedIsRetryable(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	cfg := testConfig(site)
	site.server.Close() // the port now refuses connections
	outcome := collectOnce(t, site, cfg, testClaim())
	if outcome.State != sdk.ResultRetryable || failureClassOf(t, outcome) != FailureTransport {
		t.Fatalf("state %q class %q, want retryable transport-error", outcome.State, failureClassOf(t, outcome))
	}
	assertNoCanary(t, outcome)
}

func TestCollectPageFailureMidWalkIsClassified(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	overrideStatus(site, "/wiki/api/v2/pages/1002", 503, nil)
	outcome := collectOnce(t, site, testConfig(site), testClaim())
	if outcome.State != sdk.ResultRetryable || failureClassOf(t, outcome) != FailureProviderUnavailable {
		t.Fatalf("state %q class %q, want retryable provider-unavailable", outcome.State, failureClassOf(t, outcome))
	}
	if len(outcome.Facts) != 0 {
		t.Fatal("a failed read must not emit a partial fact set as complete")
	}
}

func TestCollectLowestIndexFailureWinsAmongConcurrentFetches(t *testing.T) {
	t.Parallel()

	site := newFakeSite(t)
	for _, id := range []string{"5001", "5002", "5003", "5004"} {
		site.addPage(samplePage(id, id))
	}
	overrideStatus(site, "/wiki/api/v2/pages/5002", 429, nil)
	overrideStatus(site, "/wiki/api/v2/pages/5004", 401, nil)
	cfg := testConfig(site)
	cfg.FetchConcurrency = 4
	for i := 0; i < 5; i++ {
		outcome := collectOnce(t, site, cfg, testClaim())
		if outcome.State != sdk.ResultRetryable || failureClassOf(t, outcome) != FailureRateLimited {
			t.Fatalf("run %d: state %q class %q, want the lower-index 429 to win", i, outcome.State, failureClassOf(t, outcome))
		}
	}
}

func TestCollectDeadlineAlreadyPassedIsRetryable(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	claim := testClaim()
	claim.Deadline = testNow().Add(-time.Second)
	outcome := collectOnce(t, site, testConfig(site), claim)
	if outcome.State != sdk.ResultRetryable || failureClassOf(t, outcome) != FailureDeadline {
		t.Fatalf("state %q class %q, want retryable claim-deadline-exceeded", outcome.State, failureClassOf(t, outcome))
	}
	assertHostAccepts(t, outcome)
	if len(site.recorded()) != 0 {
		t.Error("an expired claim must not touch the provider")
	}
}

func TestCollectCancellationStopsTheReadAndIsRetryable(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	started := make(chan struct{})
	site.mu.Lock()
	site.override["/wiki/api/v2/spaces/12345"] = func(w http.ResponseWriter, r *http.Request) bool {
		close(started)
		<-r.Context().Done()
		return true
	}
	site.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-started
		cancel()
	}()
	collector := &Collector{Config: testConfig(site), Clock: fixedClock()}
	outcome := collector.Collect(ctx, testClaim())
	if outcome.State != sdk.ResultRetryable || failureClassOf(t, outcome) != FailureDeadline {
		t.Fatalf("state %q class %q, want retryable claim-deadline-exceeded", outcome.State, failureClassOf(t, outcome))
	}
}

func TestCollectClaimTimeoutBoundsASlowProvider(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	site.mu.Lock()
	site.override["/wiki/api/v2/spaces/12345"] = func(w http.ResponseWriter, r *http.Request) bool {
		<-r.Context().Done()
		return true
	}
	site.mu.Unlock()
	cfg := testConfig(site)
	cfg.ClaimTimeout = 50 * time.Millisecond
	started := time.Now()
	outcome := collectOnce(t, site, cfg, testClaim())
	if time.Since(started) > 5*time.Second {
		t.Fatal("the claim timeout did not bound the read")
	}
	if outcome.State != sdk.ResultRetryable || failureClassOf(t, outcome) != FailureDeadline {
		t.Fatalf("state %q class %q, want retryable claim-deadline-exceeded", outcome.State, failureClassOf(t, outcome))
	}
}

func TestCollectRejectsAForeignClaim(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	claim := testClaim()
	claim.CollectorKind = "jira"
	outcome := collectOnce(t, site, testConfig(site), claim)
	if outcome.State != sdk.ResultTerminal || failureClassOf(t, outcome) != FailureInvalidClaim {
		t.Fatalf("state %q class %q, want terminal invalid-claim", outcome.State, failureClassOf(t, outcome))
	}
}

// assertNoCanary proves no credential or provider body reaches any output.
func assertNoCanary(t *testing.T, outcome sdk.Result) {
	t.Helper()
	raw, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(raw), canaryToken) {
		t.Fatalf("result leaks a credential or provider body: %s", raw)
	}
}
