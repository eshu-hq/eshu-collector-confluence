// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"hash/fnv"
	"strconv"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

const (
	retryBaseDelay = time.Second
	retryMaxDelay  = time.Minute
	// deadlineRetryAfter is the retry hint when a claim ran out of time.
	deadlineRetryAfter = time.Minute
)

// base returns the result envelope shared by every state.
func base(claim sdk.Claim, state sdk.ResultState, observedAt time.Time) sdk.Result {
	return sdk.Result{
		ProtocolVersion: sdk.ProtocolVersionV1Alpha1,
		State:           state,
		Claim:           claim,
		Generation:      sdk.Generation{ID: claim.GenerationID, ObservedAt: observedAt},
	}
}

// terminalResult is the bounded-failure shape: an explicit terminal state with
// a failure class, never an ambiguous complete.
func terminalResult(claim sdk.Claim, observedAt time.Time, failureClass string) sdk.Result {
	result := base(claim, sdk.ResultTerminal, observedAt)
	result.Statuses = []sdk.Status{{Class: sdk.StatusFailure, FailureClass: failureClass}}
	return result
}

// retryableResult asks the host to release the claim for a bounded retry.
func retryableResult(claim sdk.Claim, observedAt time.Time, failureClass string, retryAfter time.Duration) sdk.Result {
	result := base(claim, sdk.ResultRetryable, observedAt)
	result.Statuses = []sdk.Status{{
		Class:             sdk.StatusFailure,
		FailureClass:      failureClass,
		RetryAfterSeconds: retryAfterSeconds(retryAfter),
	}}
	return result
}

// retryAfterSeconds rounds a delay up to whole seconds and never returns less
// than one, because the host requires a retry hint on a retryable failure.
func retryAfterSeconds(delay time.Duration) int {
	seconds := int((delay + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

// retryDelay is the provider's Retry-After when it gave one, otherwise a
// bounded exponential backoff on the claim attempt with deterministic jitter,
// so repeated attempts spread out without any random source.
func retryDelay(providerHint time.Duration, attempt int, key string) time.Duration {
	if providerHint > 0 {
		return providerHint
	}
	delay := retryBaseDelay
	for step := 1; step < attempt; step++ {
		delay *= 2
		if delay >= retryMaxDelay {
			delay = retryMaxDelay
			break
		}
	}
	return delay + retryJitter(key, attempt, delay)
}

func retryJitter(key string, attempt int, delay time.Duration) time.Duration {
	window := delay / 4
	if window <= 0 {
		return 0
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(key))
	_, _ = hash.Write([]byte(":"))
	_, _ = hash.Write([]byte(strconv.Itoa(attempt)))
	return time.Duration(hash.Sum64() % uint64(window+1)) // #nosec G115 -- result is at most window, a duration/4
}
