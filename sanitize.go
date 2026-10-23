// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"net/url"
	"regexp"
	"strings"
)

// sensitiveQueryKey mirrors the SDK's rule for query keys that must never
// appear in a source reference URI.
var sensitiveQueryKey = regexp.MustCompile(`(?i)(token|secret|password|credential|api[_-]?key|authorization)`)

// cleanText makes provider text safe to store: invalid UTF-8 becomes U+FFFD
// and NUL bytes are dropped. NUL is the one character a JSON payload can carry
// that the Postgres JSONB and text columns reject, so a single hostile page
// would otherwise poison the whole claim at commit time. Other control
// characters are preserved and JSON-escaped on the wire.
func cleanText(value string) string {
	if value == "" {
		return value
	}
	value = strings.ToValidUTF8(value, "�")
	if !strings.ContainsRune(value, 0) {
		return value
	}
	return strings.ReplaceAll(value, "\x00", "")
}

// safeSourceURI returns raw when it is a credential-free http(s) URI with a
// host, and the empty string otherwise. A source reference with userinfo, a
// sensitive-looking query key, a control character, a non-http scheme, or a
// host-local file path fails the whole result at the SDK validator, so a
// hostile provider link is replaced by the caller's fallback instead.
func safeSourceURI(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.ContainsFunc(trimmed, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.User != nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	for key := range parsed.Query() {
		if sensitiveQueryKey.MatchString(key) {
			return ""
		}
	}
	return trimmed
}
