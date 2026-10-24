// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"encoding/json"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

// Health is a debug summary of one served claim, written as one JSON line on
// stderr. It is NOT the operator signal: the extension host buffers a
// process's stderr and reports only its size (stderr_bytes), so operators do
// not see this line. It helps in a local run of the binary. The collector's
// contract with the host is the SDK status rows in the result (state, failure
// class, retry hint, fact and warning counts, source latency); the worker does
// not record them yet (eshu-hq/eshu#8908), so today only the failure class
// reaches the work item. Every field here is a count, a bounded
// class, or an opaque hash: it never carries a URL, page id, title, body,
// token, or provider response.
type Health struct {
	Component     string           `json:"component"`
	WorkItemID    string           `json:"work_item_id"`
	Attempt       int              `json:"attempt"`
	State         sdk.ResultState  `json:"state"`
	FailureClass  string           `json:"failure_class,omitempty"`
	RetryAfterSec int              `json:"retry_after_seconds,omitempty"`
	FactCount     int              `json:"fact_count"`
	WarningCount  int              `json:"warning_count,omitempty"`
	FreshnessHint string           `json:"freshness_hint,omitempty"`
	Requests      map[string]int64 `json:"requests,omitempty"`
	ElapsedMS     int64            `json:"elapsed_ms"`
}

// HealthLine summarizes a served claim as one JSON line.
func HealthLine(handled Handled, elapsed time.Duration) ([]byte, error) {
	result := handled.Result
	health := Health{
		Component:     ComponentID,
		WorkItemID:    result.Claim.WorkItemID,
		Attempt:       result.Claim.Attempt,
		State:         result.State,
		FactCount:     len(result.Facts),
		FreshnessHint: result.Generation.FreshnessHint,
		Requests:      handled.Requests,
		ElapsedMS:     elapsed.Milliseconds(),
	}
	for _, status := range result.Statuses {
		if status.Class == sdk.StatusFailure {
			health.FailureClass = status.FailureClass
			health.RetryAfterSec = status.RetryAfterSeconds
		}
		health.WarningCount += status.WarningCount
	}
	return json.Marshal(map[string]any{"health": health})
}
