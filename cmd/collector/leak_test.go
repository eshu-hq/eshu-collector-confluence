// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// echoingProvider answers every request with a failure whose body echoes the
// request's credentials and the canary, the way a misbehaving proxy or error
// page can. A collector that logs a provider body would print them.
func echoingProvider(t *testing.T, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "echo "+r.Header.Get("Authorization")+" "+secret, status)
	}))
	t.Cleanup(server.Close)
	return server
}

// deadProvider is a base URL nothing listens on.
func deadProvider(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	return url
}

func credentialEnvs() map[string]map[string]string {
	return map[string]map[string]string{
		"bearer token": {"ESHU_CONFLUENCE_BEARER_TOKEN": secret},
		"basic auth":   {"ESHU_CONFLUENCE_EMAIL": "bot@example.invalid", "ESHU_CONFLUENCE_API_TOKEN": secret},
	}
}

// TestRunNeverPrintsTheCredentialWhenTheProviderFails pins review mutant M23:
// whatever the provider does wrong, the API token and anything the provider
// echoes back stay out of stdout and stderr. Each case runs the in-process
// entry point with the token set and a failing provider.
func TestRunNeverPrintsTheCredentialWhenTheProviderFails(t *testing.T) {
	t.Parallel()

	providers := map[string]string{
		"server error":   echoingProvider(t, http.StatusInternalServerError).URL,
		"unauthorized":   echoingProvider(t, http.StatusUnauthorized).URL,
		"rate limited":   echoingProvider(t, http.StatusTooManyRequests).URL,
		"forbidden":      echoingProvider(t, http.StatusForbidden).URL,
		"connection err": deadProvider(t),
	}
	for envName, env := range credentialEnvs() {
		for providerName, baseURL := range providers {
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), []string{"--sdk-stdio"}, strings.NewReader(requestJSON(t, baseURL)), &stdout, &stderr, getenv(env))
			if err != nil {
				t.Errorf("%s/%s: run() error = %v, want a result the host records", envName, providerName, err)
			}
			for stream, text := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String(), "error": errText(err)} {
				if strings.Contains(text, secret) || strings.Contains(text, "Bearer ") || strings.Contains(text, "Basic ") {
					t.Errorf("%s/%s: %s carries a credential: %q", envName, providerName, stream, text)
				}
			}
			if stdout.Len() == 0 {
				t.Errorf("%s/%s: stdout is empty, want one SDK result", envName, providerName)
			}
		}
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestBuiltBinaryNeverPrintsTheCredential runs the compiled binary, so main()
// itself (not only run) is held to the same rule: no token on stdout or stderr
// on a provider failure or on a refused invocation.
func TestBuiltBinaryNeverPrintsTheCredential(t *testing.T) {
	t.Parallel()

	binary := builtBinary(t)
	provider := echoingProvider(t, http.StatusInternalServerError)

	tests := map[string]struct {
		args  []string
		stdin string
	}{
		"provider failure":   {args: []string{"--sdk-stdio"}, stdin: requestJSON(t, provider.URL)},
		"provider down":      {args: []string{"--sdk-stdio"}, stdin: requestJSON(t, deadProvider(t))},
		"refused invocation": {args: []string{"--sdk-stdio"}, stdin: "{"},
		"no flag":            {args: nil, stdin: requestJSON(t, provider.URL)},
	}
	for name, tt := range tests {
		command := exec.CommandContext(t.Context(), binary, tt.args...)
		command.Stdin = strings.NewReader(tt.stdin)
		command.Env = append(os.Environ(),
			"ESHU_CONFLUENCE_BEARER_TOKEN="+secret,
			"ESHU_CONFLUENCE_API_TOKEN="+secret,
			"ESHU_CONFLUENCE_EMAIL=bot@example.invalid",
		)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		_ = command.Run() // a refused invocation exits non-zero by design
		for stream, text := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
			if strings.Contains(text, secret) || strings.Contains(text, "Bearer ") {
				t.Errorf("%s: %s carries a credential: %q", name, stream, text)
			}
		}
	}
}
