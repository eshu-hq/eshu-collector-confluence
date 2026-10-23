// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

// Collector reads one claimed Confluence scope and returns one SDK result.
type Collector struct {
	// Config is the validated activation configuration.
	Config Config
	// Client reads Confluence. When nil, Collect builds an HTTP client from
	// Config and HTTP.
	Client Client
	// HTTP is the transport the built client uses; nil selects the default
	// bounded client.
	HTTP *http.Client
	// Clock returns the current time; nil selects time.Now.
	Clock func() time.Time

	stats *requestStats
}

// Requests returns provider request counts by operation and outcome for the
// last Collect call, for the stderr debug line. It holds no URL or id.
func (c *Collector) Requests() map[string]int64 {
	if c.stats == nil {
		return map[string]int64{}
	}
	return c.stats.snapshot()
}

// Collect reads the claimed scope and returns one result. Every outcome is a
// result state: complete, partial, retryable, or terminal. It never emits
// unchanged, because the host supplies no previous snapshot identity across
// claims; the core store skips a duplicate snapshot by its freshness hint.
// Collect is deterministic for a fixed clock and source, so redelivering the
// same claim returns a byte-identical result.
func (c *Collector) Collect(ctx context.Context, claim sdk.Claim) sdk.Result {
	clock := c.Clock
	if clock == nil {
		clock = time.Now
	}
	startedAt := clock().UTC()
	result := c.collect(ctx, claim, clock, startedAt)
	return stampFailureLatency(result, startedAt, clock().UTC())
}

// stampFailureLatency records on a failure status row how long the claim ran.
// The host keeps only the SDK status rows, so a retryable or terminal row must
// carry the same latency a complete row does: it is how an operator tells a
// provider that failed at once from a read that ran into the claim deadline.
func stampFailureLatency(result sdk.Result, startedAt, finishedAt time.Time) sdk.Result {
	latency := int(finishedAt.Sub(startedAt) / time.Millisecond)
	if latency <= 0 {
		return result
	}
	statuses := make([]sdk.Status, len(result.Statuses))
	copy(statuses, result.Statuses)
	for i := range statuses {
		if statuses[i].Class == sdk.StatusFailure && statuses[i].SourceLatencyMS == 0 {
			statuses[i].SourceLatencyMS = latency
		}
	}
	result.Statuses = statuses
	return result
}

func (c *Collector) collect(ctx context.Context, claim sdk.Claim, clock func() time.Time, startedAt time.Time) sdk.Result {
	if claim.CollectorKind != CollectorKind || claim.ComponentID != ComponentID {
		return terminalResult(claim, startedAt, FailureInvalidClaim)
	}

	// The host sets claim.Deadline to the initial lease expiry and kills this
	// process there; heartbeats do not extend it. Stop a margin before it so
	// the collector can return its own retryable result instead of dying
	// mid-write, and never run longer than the configured claim timeout.
	timeout := c.Config.ClaimTimeout
	if !claim.Deadline.IsZero() {
		timeout = claimBudget(timeout, claim.Deadline.Sub(startedAt))
		if timeout <= 0 {
			return retryableResult(claim, startedAt, FailureDeadline, deadlineRetryAfter)
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client, err := c.client(clock)
	if err != nil {
		return terminalResult(claim, startedAt, FailureInvalidConfig)
	}
	snap, err := readSnapshot(runCtx, client, c.Config)
	if err != nil {
		return c.failure(claim, startedAt, err)
	}
	builder := factBuilder{claim: claim, cfg: c.Config, observedAt: startedAt}
	built, err := builder.assemble(runCtx, snap)
	if err != nil {
		return c.failure(claim, startedAt, err)
	}
	return c.success(claim, startedAt, clock().UTC(), built)
}

func (c *Collector) client(clock func() time.Time) (Client, error) {
	if c.Client != nil {
		return c.Client, nil
	}
	built, err := NewHTTPClient(HTTPClientConfig{
		BaseURL:          c.Config.BaseURL,
		Credentials:      c.Config.Credentials,
		HTTP:             c.HTTP,
		MaxResponseBytes: c.Config.MaxResponseBytes,
		Clock:            clock,
	})
	if err != nil {
		return nil, err
	}
	c.stats = built.stats
	return built, nil
}

func (c *Collector) success(claim sdk.Claim, observedAt, finishedAt time.Time, built assembled) sdk.Result {
	state := sdk.ResultComplete
	status := sdk.Status{
		Class:           sdk.StatusComplete,
		FactCount:       len(built.facts),
		SourceLatencyMS: int(finishedAt.Sub(observedAt) / time.Millisecond),
	}
	if built.skipped > 0 || built.truncated {
		state = sdk.ResultPartial
		warnings := built.skipped
		if built.truncated {
			warnings++
		}
		status.Class = sdk.StatusWarning
		status.Partial = true
		status.WarningCount = warnings
	}
	result := base(claim, state, observedAt)
	result.Generation.FreshnessHint = freshnessHint(built.facts)
	result.Facts = built.facts
	result.Statuses = []sdk.Status{status}
	return result
}

// failure maps a read or build error onto a retryable or terminal result.
func (c *Collector) failure(claim sdk.Claim, observedAt time.Time, err error) sdk.Result {
	if errors.Is(err, context.DeadlineExceeded) {
		return retryableResult(claim, observedAt, FailureDeadline, deadlineRetryAfter)
	}
	if errors.Is(err, context.Canceled) {
		return retryableResult(claim, observedAt, FailureDeadline, deadlineRetryAfter)
	}
	if errors.Is(err, ErrPermissionDenied) {
		return terminalResult(claim, observedAt, FailurePermissionDenied)
	}
	var provider providerError
	if !errors.As(err, &provider) {
		return terminalResult(claim, observedAt, FailureInternal)
	}
	if !provider.retryable {
		return terminalResult(claim, observedAt, provider.class)
	}
	// A transport failure stays retryable at every attempt. The extension host
	// owns the attempt budget (10 in collector-component-extension): it ends a
	// retrying claim at that attempt and records the class of the last failure,
	// so a persistent transport failure dead-letters as transport-error. A limit
	// of this collector's own could not fire first, and claim.Attempt counts
	// every attempt, not only transport failures.
	delay := retryDelay(provider.retryAfter, claim.Attempt, retryKey(claim))
	return retryableResult(claim, observedAt, provider.class, delay)
}

// retryKey seeds the deterministic jitter with the claim's scope.
func retryKey(claim sdk.Claim) string {
	return strings.TrimSpace(claim.Scope.ID)
}
