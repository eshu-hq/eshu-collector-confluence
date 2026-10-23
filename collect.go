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

	"github.com/eshu-hq/eshu-collector-confluence/internal/httpx"
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
// last Collect call, for the operator health line. It holds no URL or id.
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
func (c *Collector) Collect(ctx context.Context, claim sdk.Claim) Outcome {
	clock := c.Clock
	if clock == nil {
		clock = time.Now
	}
	startedAt := clock().UTC()
	if !claim.Deadline.IsZero() && startedAt.After(claim.Deadline) {
		return Outcome{Result: terminalResult(claim, startedAt, FailureDeadline)}
	}
	if claim.CollectorKind != CollectorKind || claim.ComponentID != ComponentID {
		return Outcome{Result: terminalResult(claim, startedAt, FailureInvalidClaim)}
	}

	timeout := c.Config.ClaimTimeout
	if !claim.Deadline.IsZero() {
		if remaining := claim.Deadline.Sub(startedAt); remaining < timeout {
			timeout = remaining
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client, err := c.client(clock)
	if err != nil {
		return Outcome{Result: terminalResult(claim, startedAt, FailureInvalidConfig)}
	}
	snap, err := readSnapshot(runCtx, client, c.Config)
	if err != nil {
		return Outcome{Result: c.failure(claim, startedAt, err)}
	}
	builder := factBuilder{claim: claim, cfg: c.Config, observedAt: startedAt}
	built, err := builder.assemble(runCtx, snap)
	if err != nil {
		return Outcome{Result: c.failure(claim, startedAt, err)}
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

func (c *Collector) success(claim sdk.Claim, observedAt, finishedAt time.Time, built assembled) Outcome {
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
	return Outcome{Result: result, TrustedDeletion: c.Config.AllowEmptySuccessor}
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
	// A transport failure that keeps repeating is persistent misconfiguration
	// (a wrong host or port, a removed service), not an outage: surface it as
	// a terminal failure an operator can alert on instead of retrying forever.
	// claim.Attempt counts the attempts of this work item.
	if provider.transport && claim.Attempt >= httpx.MaxConsecutiveTransportFailures {
		return terminalResult(claim, observedAt, FailureTransportPersistent)
	}
	delay := retryDelay(provider.retryAfter, claim.Attempt, retryKey(claim))
	return retryableResult(claim, observedAt, provider.class, delay)
}

// retryKey seeds the deterministic jitter with the claim's scope.
func retryKey(claim sdk.Claim) string {
	return strings.TrimSpace(claim.Scope.ID)
}
