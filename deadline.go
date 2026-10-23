// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import "time"

// deadlineMarginCap caps the safety margin kept before the claim deadline.
const deadlineMarginCap = 2 * time.Second

// deadlineMargin is how long before claim.Deadline the collector stops
// starting work. The host kills the process at claim.Deadline, which it sets to
// the initial lease expiry, so the collector must finish writing its own
// result first. The margin is 5% of the time left, capped at 2 seconds, so a
// short lease still leaves most of its time for the read.
func deadlineMargin(remaining time.Duration) time.Duration {
	if remaining <= 0 {
		return 0
	}
	return min(remaining/20, deadlineMarginCap)
}

// claimBudget is the wall time one claim may spend: the configured
// claim_timeout_seconds, or the time left before the host deadline minus the
// margin, whichever is smaller. A budget of zero or less means the claim has
// no time left to start.
func claimBudget(configured, remaining time.Duration) time.Duration {
	return min(configured, remaining-deadlineMargin(remaining))
}
