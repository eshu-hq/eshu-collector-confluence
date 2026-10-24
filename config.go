// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eshu-hq/eshu-collector-confluence/internal/httpx"
)

const (
	defaultPageLimit        = 100
	maxPageLimit            = 250
	defaultMaxTotalPages    = 5000
	defaultClaimTimeoutSecs = 300
	defaultFetchConcurrency = 4
	maxFetchConcurrency     = 16
	defaultMaxPageBytes     = 1 << 20
	defaultMaxResultBytes   = 3_900_000
	defaultMaxResponseBytes = 16 << 20
	maxConfigInt            = 1 << 40

	defaultEmailEnv       = "ESHU_CONFLUENCE_EMAIL"
	defaultAPITokenEnv    = "ESHU_CONFLUENCE_API_TOKEN"
	defaultBearerTokenEnv = "ESHU_CONFLUENCE_BEARER_TOKEN"
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// Config is the validated activation configuration for one claimed scope.
// It never holds a raw secret in a field that a payload or status can reach:
// credentials sit in Credentials, whose formatting verbs redact.
type Config struct {
	// BaseURL is the Confluence site base URL without a trailing slash.
	BaseURL string
	// SpaceID selects space mode. Exactly one of SpaceID and RootPageID is set.
	SpaceID string
	// RootPageID selects page-tree mode.
	RootPageID string
	// SpaceKey is an optional label for page-tree mode.
	SpaceKey string
	// PageLimit is the per-request page size.
	PageLimit int
	// MaxTotalPages caps pages assembled across one cursor walk.
	MaxTotalPages int
	// Credentials holds the resolved read-only credentials.
	Credentials Credentials
	// ClaimTimeout bounds one claim's wall time.
	ClaimTimeout time.Duration
	// FetchConcurrency bounds concurrent page-detail fetches.
	FetchConcurrency int
	// MaxPageBytes caps one page's storage-format body; larger pages are skipped.
	MaxPageBytes int
	// MaxResultBytes caps the encoded fact bytes in one result.
	MaxResultBytes int
	// MaxResponseBytes caps one provider response body.
	MaxResponseBytes int64
}

// Credentials are the resolved Confluence read-only credentials. Bearer wins
// over basic auth, matching the in-tree collector.
type Credentials struct {
	Email       string
	APIToken    string
	BearerToken string
}

// String redacts the credentials so accidental formatting cannot leak them.
func (Credentials) String() string { return "[redacted credentials]" }

// GoString redacts the credentials for %#v formatting.
func (Credentials) GoString() string { return "confluence.Credentials{[redacted]}" }

// configError is a bounded configuration failure. It names a key or an
// environment variable NAME, never a value.
type configError struct {
	class   string
	message string
}

func (e configError) Error() string { return e.message }

// FailureClass returns the bounded failure class for the error.
func (e configError) FailureClass() string { return e.class }

func invalid(format string, args ...any) error {
	return configError{class: FailureInvalidConfig, message: fmt.Sprintf(format, args...)}
}

// ParseConfig validates the activation config map and resolves credential
// environment variables through getenv. Unknown keys are rejected so a typo
// cannot silently drop a bound or an attestation.
func ParseConfig(raw map[string]any, getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, errors.New("getenv is required")
	}
	if err := onlyKeys("config", raw, "source", "credentials", "attestation", "limits"); err != nil {
		return Config{}, err
	}
	source, err := section(raw, "source")
	if err != nil {
		return Config{}, err
	}
	if err := onlyKeys("source", source, "base_url", "space_id", "root_page_id", "space_key", "page_limit", "max_total_pages"); err != nil {
		return Config{}, err
	}
	cfg := Config{
		PageLimit:        defaultPageLimit,
		MaxTotalPages:    defaultMaxTotalPages,
		ClaimTimeout:     defaultClaimTimeoutSecs * time.Second,
		FetchConcurrency: defaultFetchConcurrency,
		MaxPageBytes:     defaultMaxPageBytes,
		MaxResultBytes:   defaultMaxResultBytes,
		MaxResponseBytes: defaultMaxResponseBytes,
	}
	if err := parseSource(&cfg, source); err != nil {
		return Config{}, err
	}
	if err := parseLimits(&cfg, raw); err != nil {
		return Config{}, err
	}
	if err := parseAttestation(raw); err != nil {
		return Config{}, err
	}
	return cfg, resolveCredentials(&cfg, raw, getenv)
}

func parseSource(cfg *Config, source map[string]any) error {
	baseURL, err := stringValue(source, "source", "base_url")
	if err != nil {
		return err
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return invalid("source.base_url is required")
	}
	parsed, err := httpx.ParseBaseURL("confluence", baseURL)
	if err != nil {
		return invalid("source.base_url is invalid: %v", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return invalid("source.base_url must use http or https")
	}
	if safeSourceURI(baseURL) == "" {
		return invalid("source.base_url must be a credential-free http or https URL")
	}
	cfg.BaseURL = baseURL
	if cfg.SpaceID, err = stringValue(source, "source", "space_id"); err != nil {
		return err
	}
	if cfg.RootPageID, err = stringValue(source, "source", "root_page_id"); err != nil {
		return err
	}
	if cfg.SpaceKey, err = stringValue(source, "source", "space_key"); err != nil {
		return err
	}
	cfg.SpaceID = strings.TrimSpace(cfg.SpaceID)
	cfg.RootPageID = strings.TrimSpace(cfg.RootPageID)
	cfg.SpaceKey = strings.TrimSpace(cfg.SpaceKey)
	switch {
	case cfg.SpaceID == "" && cfg.RootPageID == "":
		return invalid("source.space_id or source.root_page_id is required")
	case cfg.SpaceID != "" && cfg.RootPageID != "":
		return invalid("configure only one of source.space_id or source.root_page_id")
	}
	if err := positiveInt(source, "source", "page_limit", maxPageLimit, &cfg.PageLimit); err != nil {
		return err
	}
	return positiveInt(source, "source", "max_total_pages", maxConfigInt, &cfg.MaxTotalPages)
}

func parseLimits(cfg *Config, raw map[string]any) error {
	limits, err := section(raw, "limits")
	if err != nil {
		return err
	}
	if err := onlyKeys("limits", limits, "claim_timeout_seconds", "fetch_concurrency", "max_page_bytes", "max_result_bytes", "max_response_bytes"); err != nil {
		return err
	}
	seconds := int(cfg.ClaimTimeout / time.Second)
	if err := positiveInt(limits, "limits", "claim_timeout_seconds", 24*3600, &seconds); err != nil {
		return err
	}
	cfg.ClaimTimeout = time.Duration(seconds) * time.Second
	if err := positiveInt(limits, "limits", "fetch_concurrency", maxFetchConcurrency, &cfg.FetchConcurrency); err != nil {
		return err
	}
	if err := positiveInt(limits, "limits", "max_page_bytes", 1<<30, &cfg.MaxPageBytes); err != nil {
		return err
	}
	if err := positiveInt(limits, "limits", "max_result_bytes", 1<<30, &cfg.MaxResultBytes); err != nil {
		return err
	}
	responseBytes := int(cfg.MaxResponseBytes)
	if err := positiveInt(limits, "limits", "max_response_bytes", 1<<30, &responseBytes); err != nil {
		return err
	}
	cfg.MaxResponseBytes = int64(responseBytes)
	return nil
}

// parseAttestation reads attestation.allow_empty_successor. The in-tree
// option attests that an empty or sharply shrunken successor generation is
// legitimate, which the host receives as generation.trusted_deletion. The
// published collector SDK (v0.2.0) has no such field, and this collector does
// not marshal unpublished wire fields, so true fails closed with a terminal
// unsupported_option instead of being ignored. Absent and false behave the
// same: no attestation.
//
// Remove the refusal when a collector SDK release carries
// Generation.TrustedDeletion and this module depends on it.
func parseAttestation(raw map[string]any) error {
	attestation, err := section(raw, "attestation")
	if err != nil {
		return err
	}
	if err := onlyKeys("attestation", attestation, "allow_empty_successor"); err != nil {
		return err
	}
	value, present := attestation["allow_empty_successor"]
	if !present {
		return nil
	}
	allowed, ok := value.(bool)
	if !ok {
		return invalid("attestation.allow_empty_successor must be a boolean")
	}
	if allowed {
		return configError{
			class: FailureUnsupportedOption,
			message: "attestation.allow_empty_successor is not supported: the published collector SDK " +
				"has no generation.trusted_deletion field; remove the option until an SDK release carries it",
		}
	}
	return nil
}

func resolveCredentials(cfg *Config, raw map[string]any, getenv func(string) string) error {
	block, err := section(raw, "credentials")
	if err != nil {
		return err
	}
	if err := onlyKeys("credentials", block, "email_env", "api_token_env", "bearer_token_env"); err != nil {
		return err
	}
	names := map[string]string{
		"email_env":        defaultEmailEnv,
		"api_token_env":    defaultAPITokenEnv,
		"bearer_token_env": defaultBearerTokenEnv,
	}
	for key := range names {
		override, err := stringValue(block, "credentials", key)
		if err != nil {
			return err
		}
		if strings.TrimSpace(override) == "" {
			continue
		}
		if !envNamePattern.MatchString(strings.TrimSpace(override)) {
			return invalid("credentials.%s must be an environment variable name", key)
		}
		names[key] = strings.TrimSpace(override)
	}
	cfg.Credentials = Credentials{
		Email:       strings.TrimSpace(getenv(names["email_env"])),
		APIToken:    strings.TrimSpace(getenv(names["api_token_env"])),
		BearerToken: strings.TrimSpace(getenv(names["bearer_token_env"])),
	}
	if cfg.Credentials.BearerToken == "" && (cfg.Credentials.Email == "" || cfg.Credentials.APIToken == "") {
		return configError{
			class: FailureMissingCredentials,
			message: fmt.Sprintf(
				"read-only Confluence credentials are required: set %s, or both %s and %s",
				names["bearer_token_env"], names["email_env"], names["api_token_env"],
			),
		}
	}
	return nil
}

// ClassifyConfigError returns the bounded failure class for a ParseConfig error.
func ClassifyConfigError(err error) string {
	var ce configError
	if errors.As(err, &ce) {
		return ce.class
	}
	return FailureInvalidConfig
}

func section(raw map[string]any, key string) (map[string]any, error) {
	value, present := raw[key]
	if !present || value == nil {
		return map[string]any{}, nil
	}
	typed, ok := value.(map[string]any)
	if !ok {
		return nil, invalid("%s must be an object", key)
	}
	return typed, nil
}

func onlyKeys(path string, block map[string]any, allowed ...string) error {
	known := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		known[key] = struct{}{}
	}
	var unknown []string
	for key := range block {
		if _, ok := known[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return invalid("%s has unsupported keys: %s", path, strings.Join(boundedKeys(unknown), ", "))
}

// boundedKeys keeps an unknown-key error short and free of control characters.
func boundedKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		cleaned := strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, key)
		if len(cleaned) > 48 {
			cleaned = cleaned[:48]
		}
		out = append(out, cleaned)
		if len(out) == 5 {
			break
		}
	}
	return out
}

func stringValue(block map[string]any, path, key string) (string, error) {
	value, present := block[key]
	if !present || value == nil {
		return "", nil
	}
	switch typed := value.(type) {
	case string:
		return typed, nil
	case json.Number:
		return typed.String(), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case float64:
		// Numeric ids written without quotes in YAML are accepted.
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	default:
		return "", invalid("%s.%s must be a string", path, key)
	}
}

func positiveInt(block map[string]any, path, key string, upper int, target *int) error {
	value, present := block[key]
	if !present || value == nil {
		return nil
	}
	var number float64
	switch typed := value.(type) {
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case float64:
		number = typed
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return invalid("%s.%s must be a positive integer", path, key)
		}
		number = parsed
	default:
		return invalid("%s.%s must be a number", path, key)
	}
	if number != math.Trunc(number) || number <= 0 || number > float64(upper) {
		return invalid("%s.%s must be a positive integer no greater than %d", path, key, upper)
	}
	*target = int(number)
	return nil
}
