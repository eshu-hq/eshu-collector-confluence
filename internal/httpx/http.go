// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package httpx

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultHTTPTimeout = 30 * time.Second

// ParseBaseURL validates a provider base URL without checking credentials.
// It rejects a missing scheme or host and any embedded userinfo.
//
// TODO(eshu-hq/eshu#8851): replace with the published eshu-sdk helper.
func ParseBaseURL(provider string, rawURL string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawURL), "/"))
	if err != nil {
		return nil, fmt.Errorf("parse %s base_url: %s", strings.TrimSpace(provider), "malformed URL")
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("%s base_url must include scheme and host", strings.TrimSpace(provider))
	}
	if base.User != nil {
		return nil, fmt.Errorf("%s base_url must not include credentials", strings.TrimSpace(provider))
	}
	return base, nil
}

// DefaultClient returns the default bounded collector HTTP client.
//
// TODO(eshu-hq/eshu#8851): replace with the published eshu-sdk helper.
func DefaultClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	return &http.Client{Timeout: timeout}
}

// ParseRetryAfter parses a Retry-After seconds or HTTP-date header value
// against now. It returns zero for an empty, malformed, or elapsed value.
//
// TODO(eshu-hq/eshu#8851): replace with the published eshu-sdk helper.
func ParseRetryAfter(raw string, now time.Time) time.Duration {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(trimmed); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if now.IsZero() {
		now = time.Now()
	}
	if value, err := http.ParseTime(trimmed); err == nil {
		delay := value.Sub(now)
		if delay > 0 {
			return delay
		}
	}
	return 0
}
