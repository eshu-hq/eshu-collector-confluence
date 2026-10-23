// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"errors"
	"strconv"
	"time"
)

// ErrPermissionDenied marks a Confluence page or space the read-only
// credential cannot view (HTTP 403 or 404).
var ErrPermissionDenied = errors.New("confluence permission denied")

// providerError is a bounded Confluence read failure. Its text carries only
// the failure class and an HTTP status code: never a URL, page id, title,
// response body, or credential. cause is kept for transport classification
// and is deliberately not part of Error().
type providerError struct {
	class      string
	retryable  bool
	statusCode int
	retryAfter time.Duration
	cause      error
}

func (e providerError) Error() string {
	if e.statusCode > 0 {
		return "confluence " + e.class + " (status " + strconv.Itoa(e.statusCode) + ")"
	}
	return "confluence " + e.class
}

// Unwrap exposes the transport cause to errors.Is and errors.As only.
func (e providerError) Unwrap() error { return e.cause }
