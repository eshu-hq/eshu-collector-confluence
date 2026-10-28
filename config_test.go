// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func envFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func validRaw() map[string]any {
	return map[string]any{
		"source": map[string]any{
			"base_url": "https://confluence.example.invalid/wiki/",
			"space_id": "12345",
		},
	}
}

var basicEnv = map[string]string{
	"ESHU_CONFLUENCE_EMAIL":     "bot@example.invalid",
	"ESHU_CONFLUENCE_API_TOKEN": canaryToken,
}

func TestParseConfigAppliesDocumentedDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfig(validRaw(), envFrom(basicEnv))
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	if cfg.BaseURL != "https://confluence.example.invalid/wiki" {
		t.Errorf("BaseURL = %q, want the trailing slash trimmed", cfg.BaseURL)
	}
	if cfg.PageLimit != 100 || cfg.MaxTotalPages != 5000 || cfg.FetchConcurrency != 4 ||
		cfg.ClaimTimeout != 300*time.Second || cfg.MaxPageBytes != 1<<20 ||
		cfg.MaxResultBytes != defaultMaxResultBytes || cfg.MaxResponseBytes != 16<<20 {
		t.Errorf("defaults = %+v", cfg)
	}
}

func TestParseConfigAcceptsRootPageAndNumericYAMLIDs(t *testing.T) {
	t.Parallel()

	raw := map[string]any{"source": map[string]any{
		"base_url":     "https://confluence.example.invalid",
		"root_page_id": 98765432101.0, // YAML numbers arrive as float64 after a JSON round trip
		"space_key":    "ENG",
	}}
	cfg, err := ParseConfig(raw, envFrom(basicEnv))
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	if cfg.RootPageID != "98765432101" || cfg.SpaceID != "" || cfg.SpaceKey != "ENG" {
		t.Errorf("config = %+v, want a page-tree scope with the numeric id intact", cfg)
	}
}

func TestParseConfigReadsEveryOverride(t *testing.T) {
	t.Parallel()

	raw := validRaw()
	raw["source"].(map[string]any)["page_limit"] = 50
	raw["source"].(map[string]any)["max_total_pages"] = float64(750)
	raw["limits"] = map[string]any{
		"claim_timeout_seconds": 60, "fetch_concurrency": 2, "max_page_bytes": 2048,
		"max_result_bytes": 1_000_000, "max_response_bytes": 4096,
	}
	cfg, err := ParseConfig(raw, envFrom(basicEnv))
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	if cfg.PageLimit != 50 || cfg.MaxTotalPages != 750 || cfg.ClaimTimeout != time.Minute ||
		cfg.FetchConcurrency != 2 || cfg.MaxPageBytes != 2048 || cfg.MaxResultBytes != 1_000_000 || cfg.MaxResponseBytes != 4096 {
		t.Errorf("config = %+v", cfg)
	}
}

func TestParseConfigRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	with := func(mutate func(map[string]any)) map[string]any {
		raw := validRaw()
		mutate(raw)
		return raw
	}
	source := func(raw map[string]any) map[string]any { return raw["source"].(map[string]any) }
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"no source", map[string]any{}, "source.base_url is required"},
		{"source wrong type", map[string]any{"source": "x"}, "source must be an object"},
		{"no base url", with(func(r map[string]any) { delete(source(r), "base_url") }), "source.base_url is required"},
		{"base url userinfo", with(func(r map[string]any) { source(r)["base_url"] = "https://u:p@confluence.example.invalid" }), "must not include credentials"},
		{"base url sensitive query", with(func(r map[string]any) { source(r)["base_url"] = "https://confluence.example.invalid/wiki?token=abc" }), "credential-free"},
		{"base url scheme", with(func(r map[string]any) { source(r)["base_url"] = "ftp://confluence.example.invalid" }), "must use http or https"},
		{"no scope", with(func(r map[string]any) { delete(source(r), "space_id") }), "space_id or source.root_page_id is required"},
		{"both scopes", with(func(r map[string]any) { source(r)["root_page_id"] = "1" }), "only one of"},
		{"unknown top key", with(func(r map[string]any) { r["freshness"] = map[string]any{} }), "config has unsupported keys: freshness"},
		{"unknown source key", with(func(r map[string]any) { source(r)["space_ids"] = "1,2" }), "source has unsupported keys: space_ids"},
		{"unknown attestation key", with(func(r map[string]any) { r["attestation"] = map[string]any{"allow_empty_successer": true} }), "attestation has unsupported keys"},
		{"attestation not bool", with(func(r map[string]any) { r["attestation"] = map[string]any{"allow_empty_successor": "yes"} }), "must be a boolean"},
		{"zero page limit", with(func(r map[string]any) { source(r)["page_limit"] = 0 }), "positive integer"},
		{"page limit over cap", with(func(r map[string]any) { source(r)["page_limit"] = 1000 }), "no greater than 250"},
		{"fractional pages", with(func(r map[string]any) { source(r)["max_total_pages"] = 10.5 }), "positive integer"},
		{"negative pages", with(func(r map[string]any) { source(r)["max_total_pages"] = -1 }), "positive integer"},
		{"pages as string", with(func(r map[string]any) { source(r)["max_total_pages"] = "10" }), "must be a number"},
		{"concurrency over cap", with(func(r map[string]any) { r["limits"] = map[string]any{"fetch_concurrency": 99} }), "no greater than 16"},
		{"unknown limit", with(func(r map[string]any) { r["limits"] = map[string]any{"max_pages": 1} }), "limits has unsupported keys"},
		{"bad env name", with(func(r map[string]any) { r["credentials"] = map[string]any{"email_env": "BAD NAME;rm"} }), "environment variable name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseConfig(tt.raw, envFrom(basicEnv))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseConfig() error = %v, want containing %q", err, tt.want)
			}
			if ClassifyConfigError(err) != FailureInvalidConfig {
				t.Errorf("class = %q, want invalid-config", ClassifyConfigError(err))
			}
		})
	}
}

func TestParseConfigUnknownKeyErrorIsBounded(t *testing.T) {
	t.Parallel()

	raw := validRaw()
	for i := 0; i < 30; i++ {
		raw[fmt.Sprintf("k%02d\n\x1b[31m%s", i, strings.Repeat("x", 100))] = 1
	}
	_, err := ParseConfig(raw, envFrom(basicEnv))
	if err == nil {
		t.Fatal("ParseConfig() error = nil, want a refusal")
	}
	if len(err.Error()) > 500 || strings.ContainsAny(err.Error(), "\n\x1b") {
		t.Errorf("error = %q, want it short and free of control characters", err)
	}
}

func TestParseConfigResolvesCredentialsFromNamedVariables(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     map[string]any
		env     map[string]string
		want    Credentials
		wantErr string
	}{
		{name: "default basic names", env: basicEnv, want: Credentials{Email: "bot@example.invalid", APIToken: canaryToken}},
		{name: "bearer from default name", env: map[string]string{"ESHU_CONFLUENCE_BEARER_TOKEN": canaryToken}, want: Credentials{BearerToken: canaryToken}},
		{
			name: "custom names",
			raw:  map[string]any{"email_env": "SITE_USER", "api_token_env": "SITE_TOKEN"},
			env:  map[string]string{"SITE_USER": "u@example.invalid", "SITE_TOKEN": canaryToken},
			want: Credentials{Email: "u@example.invalid", APIToken: canaryToken},
		},
		{
			name: "custom bearer name",
			raw:  map[string]any{"bearer_token_env": "SITE_BEARER"},
			env:  map[string]string{"SITE_BEARER": canaryToken},
			want: Credentials{BearerToken: canaryToken},
		},
		{name: "values are trimmed", env: map[string]string{"ESHU_CONFLUENCE_BEARER_TOKEN": "  " + canaryToken + "\n"}, want: Credentials{BearerToken: canaryToken}},
		{name: "nothing set", env: map[string]string{}, wantErr: "ESHU_CONFLUENCE_BEARER_TOKEN"},
		{name: "email without token", env: map[string]string{"ESHU_CONFLUENCE_EMAIL": "bot@example.invalid"}, wantErr: "ESHU_CONFLUENCE_API_TOKEN"},
		{name: "token without email", env: map[string]string{"ESHU_CONFLUENCE_API_TOKEN": canaryToken}, wantErr: "ESHU_CONFLUENCE_EMAIL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw := validRaw()
			if tt.raw != nil {
				raw["credentials"] = tt.raw
			}
			cfg, err := ParseConfig(raw, envFrom(tt.env))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseConfig() error = %v, want naming %q", err, tt.wantErr)
				}
				if ClassifyConfigError(err) != FailureMissingCredentials {
					t.Errorf("class = %q, want missing-credentials", ClassifyConfigError(err))
				}
				if strings.Contains(err.Error(), canaryToken) {
					t.Errorf("error %q leaks a credential value", err)
				}
				return
			}
			if err != nil || cfg.Credentials != tt.want {
				t.Fatalf("ParseConfig() = %+v, %v, want credentials %+v", cfg.Credentials, err, tt.want)
			}
		})
	}
}

func TestCredentialsNeverFormatTheirSecrets(t *testing.T) {
	t.Parallel()

	creds := Credentials{Email: "bot@example.invalid", APIToken: canaryToken, BearerToken: canaryToken}
	cfg := Config{Credentials: creds}
	for _, rendered := range []string{
		fmt.Sprintf("%v", creds), fmt.Sprintf("%+v", creds), fmt.Sprintf("%#v", creds), creds.String(),
		fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg),
	} {
		if strings.Contains(rendered, canaryToken) || strings.Contains(rendered, "bot@example.invalid") {
			t.Errorf("formatting leaked a credential: %q", rendered)
		}
	}
}

func TestParseConfigRefusesTheEmptySuccessorAttestation(t *testing.T) {
	t.Parallel()

	raw := validRaw()
	raw["attestation"] = map[string]any{"allow_empty_successor": true}
	_, err := ParseConfig(raw, envFrom(basicEnv))
	if err == nil || ClassifyConfigError(err) != FailureUnsupportedOption {
		t.Fatalf("ParseConfig() error = %v class %q, want unsupported_option", err, ClassifyConfigError(err))
	}
	if !strings.Contains(err.Error(), "allow_empty_successor") {
		t.Errorf("error %q does not name the option", err)
	}
	for _, value := range []any{false, nil} {
		raw["attestation"] = map[string]any{"allow_empty_successor": value}
		if value == nil {
			raw["attestation"] = map[string]any{}
		}
		if _, err := ParseConfig(raw, envFrom(basicEnv)); err != nil {
			t.Errorf("allow_empty_successor %v: ParseConfig() error = %v, want it accepted", value, err)
		}
	}
}
