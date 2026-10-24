// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

// deadlineProbe is a Client that records the deadline of the context the
// collector hands to the first provider read, then fails the read.
type deadlineProbe struct {
	mu       sync.Mutex
	deadline time.Time
	seen     bool
	// err is what every read returns; nil means errProbeDone.
	err error
}

var errProbeDone = errors.New("probe done")

func (p *deadlineProbe) GetSpace(ctx context.Context, _ string) (Space, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deadline, p.seen = ctx.Deadline()
	return Space{}, p.fail()
}

func (p *deadlineProbe) fail() error {
	if p.err != nil {
		return p.err
	}
	return errProbeDone
}

func (p *deadlineProbe) ListSpacePages(context.Context, string, int, int) ([]Page, bool, error) {
	return nil, false, p.fail()
}

func (p *deadlineProbe) ListPageTree(context.Context, string, int, int) ([]string, bool, error) {
	return nil, false, p.fail()
}

func (p *deadlineProbe) GetPage(context.Context, string) (Page, error) {
	return Page{}, p.fail()
}

// readDeadline runs Collect with a fixed clock so the claim's remaining time is
// exact, and returns how far from the real start the read's context expires.
func readDeadline(t *testing.T, configured, remaining time.Duration) time.Duration {
	t.Helper()
	probe := &deadlineProbe{}
	claim := testClaim()
	claim.Deadline = testNow().Add(remaining)
	cfg := Config{SpaceID: "12345", ClaimTimeout: configured, PageLimit: 100, MaxTotalPages: 10, FetchConcurrency: 1}
	collector := &Collector{Config: cfg, Client: probe, Clock: fixedClock()}
	started := time.Now()
	collector.Collect(t.Context(), claim)
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if !probe.seen {
		t.Fatal("the provider read ran without a context deadline")
	}
	return probe.deadline.Sub(started)
}

// TestCollectStopsBeforeTheHostDeadline pins review mutant M17. The host kills
// the process at claim.Deadline, so the collector must give up a margin before
// it; removing the clamp (or the margin) must fail this test.
func TestCollectStopsBeforeTheHostDeadline(t *testing.T) {
	t.Parallel()

	const remaining = 40 * time.Second
	got := readDeadline(t, time.Hour, remaining)
	wantMax := remaining - deadlineMarginCap + 500*time.Millisecond // scheduling slack
	wantMin := remaining - deadlineMarginCap - 500*time.Millisecond
	if got > wantMax || got < wantMin {
		t.Fatalf("read context expires in %v, want about %v (claim deadline %v minus the %v margin)",
			got, remaining-deadlineMarginCap, remaining, deadlineMarginCap)
	}
}

func TestCollectKeepsTheSmallerConfiguredTimeout(t *testing.T) {
	t.Parallel()

	got := readDeadline(t, 5*time.Second, time.Hour)
	if got > 5500*time.Millisecond || got < 4500*time.Millisecond {
		t.Fatalf("read context expires in %v, want about the configured 5s", got)
	}
}

func TestCollectMarginScalesDownForShortLeases(t *testing.T) {
	t.Parallel()

	// 5% of 10s is 500ms, below the 2s cap.
	got := readDeadline(t, time.Hour, 10*time.Second)
	if got > 9500*time.Millisecond+500*time.Millisecond || got < 9500*time.Millisecond-500*time.Millisecond {
		t.Fatalf("read context expires in %v, want about 9.5s", got)
	}
}

func TestCollectReturnsItsOwnRetryableResultAtTheMargin(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	site.mu.Lock()
	site.override["/wiki/api/v2/spaces/12345"] = func(_ http.ResponseWriter, r *http.Request) bool {
		<-r.Context().Done()
		return true
	}
	site.mu.Unlock()
	cfg := testConfig(site)
	cfg.ClaimTimeout = time.Hour
	claim := testClaim()
	claim.Deadline = testNow().Add(300 * time.Millisecond)
	// The parent context is a long backstop: only the clamp can end the read
	// well before it.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	started := time.Now()
	collector := &Collector{Config: cfg, Clock: fixedClock()}
	outcome := collector.Collect(ctx, claim)
	if time.Since(started) > 5*time.Second {
		t.Fatal("the read ran past the claim deadline instead of stopping at the margin")
	}
	if outcome.State != sdk.ResultRetryable || failureClassOf(t, outcome) != FailureDeadline {
		t.Fatalf("state %q class %q, want retryable %s", outcome.State, failureClassOf(t, outcome), FailureDeadline)
	}
	assertHostAccepts(t, outcome)
}

func TestDeadlineMargin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		remaining, want time.Duration
	}{
		{remaining: 60 * time.Second, want: 2 * time.Second},
		{remaining: 40 * time.Second, want: 2 * time.Second},
		{remaining: 10 * time.Second, want: 500 * time.Millisecond},
		{remaining: 40 * time.Millisecond, want: 2 * time.Millisecond},
		{remaining: 0, want: 0},
		{remaining: -time.Second, want: 0},
	}
	for _, tt := range tests {
		if got := deadlineMargin(tt.remaining); got != tt.want {
			t.Errorf("deadlineMargin(%v) = %v, want %v", tt.remaining, got, tt.want)
		}
	}
}

// steppingClock advances by step on every reading, so the elapsed time Collect
// measures is a known number of milliseconds.
func steppingClock(step time.Duration) func() time.Time {
	var mu sync.Mutex
	now := testNow()
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		reading := now
		now = now.Add(step)
		return reading
	}
}

// TestFailureStatusRowsCarryTheSourceLatency covers the operator signal. The
// host keeps only the SDK status rows (it buffers stderr and reports its size),
// so a retryable or terminal row must say how long the claim ran before it
// failed, the same field the success row carries.
func TestFailureStatusRowsCarryTheSourceLatency(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err       error
		wantState sdk.ResultState
		wantClass string
	}{
		"retryable provider outage": {
			err:       providerError{class: FailureProviderUnavailable, retryable: true, statusCode: 503},
			wantState: sdk.ResultRetryable, wantClass: FailureProviderUnavailable,
		},
		"terminal credential": {
			err:       providerError{class: FailureAuthFailed, statusCode: 401},
			wantState: sdk.ResultTerminal, wantClass: FailureAuthFailed,
		},
		"claim deadline": {
			err:       context.DeadlineExceeded,
			wantState: sdk.ResultRetryable, wantClass: FailureDeadline,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{SpaceID: "12345", ClaimTimeout: time.Minute, PageLimit: 100, MaxTotalPages: 10, FetchConcurrency: 1}
			collector := &Collector{Config: cfg, Client: &deadlineProbe{err: tt.err}, Clock: steppingClock(1500 * time.Millisecond)}
			outcome := collector.Collect(t.Context(), testClaim())
			if outcome.State != tt.wantState || failureClassOf(t, outcome) != tt.wantClass {
				t.Fatalf("state %q class %q, want %q %q", outcome.State, failureClassOf(t, outcome), tt.wantState, tt.wantClass)
			}
			if got := outcome.Statuses[0].SourceLatencyMS; got != 1500 {
				t.Errorf("SourceLatencyMS = %d, want 1500", got)
			}
			assertHostAccepts(t, outcome)
		})
	}
}

func TestSuccessStatusRowKeepsItsLatencyAndCounts(t *testing.T) {
	t.Parallel()

	site := twoPageSite(t)
	collector := &Collector{Config: testConfig(site), Clock: steppingClock(250 * time.Millisecond)}
	outcome := collector.Collect(t.Context(), testClaim())
	if outcome.State != sdk.ResultComplete {
		t.Fatalf("State = %q, want complete", outcome.State)
	}
	status := outcome.Statuses[0]
	if status.SourceLatencyMS <= 0 || status.FactCount != len(outcome.Facts) {
		t.Errorf("status = %+v, want a latency and the fact count", status)
	}
}
