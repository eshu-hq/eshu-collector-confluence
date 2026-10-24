// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package httpx

import (
	"strings"
	"testing"
	"time"
)

func TestParseBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "https host", raw: "https://confluence.example.invalid/wiki/"},
		{name: "missing scheme", raw: "confluence.example.invalid", wantErr: "must include scheme and host"},
		{name: "missing host", raw: "https://", wantErr: "must include scheme and host"},
		{name: "userinfo", raw: "https://user:secret@confluence.example.invalid", wantErr: "must not include credentials"},
		{name: "unparseable", raw: "https://confluence.example.invalid:bad port", wantErr: "malformed URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base, err := ParseBaseURL("confluence", tt.raw)
			if tt.wantErr == "" {
				if err != nil || base == nil {
					t.Fatalf("ParseBaseURL() = %v, %v, want success", base, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ParseBaseURL() error = %v, want containing %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error %q leaked the credential", err)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		raw  string
		want time.Duration
	}{
		{"", 0},
		{"7", 7 * time.Second},
		{" 30 ", 30 * time.Second},
		{"0", 0},
		{"-4", 0},
		{"soon", 0},
		{now.Add(90 * time.Second).Format("Mon, 02 Jan 2006 15:04:05 GMT"), 90 * time.Second},
		{now.Add(-time.Minute).Format("Mon, 02 Jan 2006 15:04:05 GMT"), 0},
	}
	for _, tt := range tests {
		if got := ParseRetryAfter(tt.raw, now); got != tt.want {
			t.Errorf("ParseRetryAfter(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

func TestDefaultClientAppliesDefaultTimeout(t *testing.T) {
	t.Parallel()

	if got := DefaultClient(0).Timeout; got != defaultHTTPTimeout {
		t.Fatalf("DefaultClient(0).Timeout = %v, want %v", got, defaultHTTPTimeout)
	}
	if got := DefaultClient(time.Second).Timeout; got != time.Second {
		t.Fatalf("DefaultClient(1s).Timeout = %v, want 1s", got)
	}
}
