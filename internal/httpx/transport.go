// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

// Package httpx holds the bounded HTTP and transient-transport helpers this
// collector shares across its client and retry code.
//
// Replace this package with the published eshu-sdk helper when
// eshu-hq/eshu#8851 lands. Until then it is a copy of the pieces this collector
// uses from go/internal/collector/sdk (transport.go and the base-URL,
// default-client, and Retry-After helpers in http.go), with two deliberate
// differences. ParseBaseURL returns the fixed error text "malformed URL"
// instead of wrapping url.Parse's error, because that error echoes the URL,
// userinfo included. The MaxConsecutiveTransportFailures constant is gone,
// because the extension host owns the attempt budget (see the README).
package httpx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"syscall"
)

// transientErrnos are the socket-level failures that a healthy provider
// produces during load balancer rotations, idle-connection reaping, and pod
// restarts. Each one is a property of the connection, not of the request, so
// the same request is expected to succeed on the next attempt.
var transientErrnos = []error{
	syscall.ECONNRESET,
	syscall.ECONNREFUSED,
	syscall.ECONNABORTED,
	syscall.EPIPE,
	syscall.ETIMEDOUT,
	syscall.EHOSTUNREACH,
	syscall.ENETUNREACH,
}

// IsTransientTransportError reports whether err is a transient connection-level
// failure that a collector should retry after backoff instead of treating as a
// fatal source error: connection reset/refused/aborted, broken pipe, an
// unexpected or premature EOF, or a network timeout (including a per-request
// http.Client timeout).
//
// It never reports true when the parent ctx is already done or when err wraps
// context.Canceled, so operator shutdown and caller cancellation are never
// retried. TLS or certificate failures, DNS "not found", malformed responses,
// and HTTP status errors are not transport errors and return false; status
// handling stays with the caller.
//
// A bare io.EOF is transient only when it is wrapped in a *url.Error, which is
// how net/http reports a server that closed the connection before sending a
// response. The same bare io.EOF from a JSON decoder reading a 200 response with
// an empty body (a proxy, WAF, or misrouted base URL) is a persistent content
// problem, not a dropped connection, and returns false; a body cut short
// mid-response is io.ErrUnexpectedEOF and stays transient. The function does
// not bound retries; the caller (here, the extension host's attempt budget)
// does.
func IsTransientTransportError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, io.EOF) {
		var urlErr *url.Error
		return errors.As(err, &urlErr)
	}
	for _, errno := range transientErrnos {
		if errors.Is(err, errno) {
			return true
		}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsTimeout || dnsErr.IsTemporary
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

// TransportFailureClass names the cause of a transport error as a bounded,
// low-cardinality string safe for logs and metric labels: "reset", "refused",
// "timeout", "unreachable", "eof", "dns", or "other". It never includes hosts,
// URLs, or error text.
func TransportFailureClass(err error) string {
	switch {
	case err == nil:
		return "other"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, syscall.ECONNABORTED),
		errors.Is(err, syscall.EPIPE):
		return "reset"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "unreachable"
	case errors.Is(err, syscall.ETIMEDOUT):
		return "timeout"
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "eof"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return "other"
}
