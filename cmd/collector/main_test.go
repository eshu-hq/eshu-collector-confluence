// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"

	confluence "github.com/eshu-hq/eshu-collector-confluence"
)

const secret = "cmd-canary-token-42"

// confluenceStub serves one space with one page.
func confluenceStub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	page := map[string]any{
		"id": "1001", "status": "current", "title": "Runbook", "spaceId": "12345",
		"version": map[string]any{"number": 2, "createdAt": "2026-09-01T10:00:00.000Z"},
		"body":    map[string]any{"storage": map[string]any{"value": `<p><a href="https://docs.example.invalid/x">x</a></p>`, "representation": "storage"}},
		"_links":  map[string]any{"webui": "/spaces/ENG/pages/1001"},
	}
	mux.HandleFunc("/wiki/api/v2/spaces/12345", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"id": "12345", "key": "ENG", "name": "Engineering"})
	})
	mux.HandleFunc("/wiki/api/v2/spaces/12345/pages", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"results": []any{page}, "_links": map[string]any{}})
	})
	mux.HandleFunc("/wiki/api/v2/pages/1001", func(w http.ResponseWriter, r *http.Request) { write(w, page) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func requestJSON(t *testing.T, baseURL string) string {
	t.Helper()
	request := confluence.Request{
		ProtocolVersion: sdk.ProtocolVersionV1Alpha1,
		Claim: sdk.Claim{
			ComponentID: confluence.ComponentID, InstanceID: "confluence-eng", CollectorKind: confluence.CollectorKind,
			SourceSystem: confluence.SourceSystem,
			Scope:        sdk.Scope{ID: "doc-source:confluence:confluence.example.invalid:12345", Kind: "documentation_source"},
			SourceRunID:  "component-generation:abc", GenerationID: "component-generation:abc", WorkItemID: "component-work:abc",
			FencingToken: "7", Attempt: 1, Deadline: time.Now().Add(time.Hour), ConfigHandle: "component-config:confluence",
		},
		Contract: confluence.Contract(),
		Config:   map[string]any{"source": map[string]any{"base_url": baseURL + "/wiki", "space_id": "12345"}},
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal(request) error = %v", err)
	}
	return string(raw)
}

func getenv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestRunStdioWritesExactlyOneResultToStdout(t *testing.T) {
	t.Parallel()

	server := confluenceStub(t)
	var stdout, stderr bytes.Buffer
	env := getenv(map[string]string{"ESHU_CONFLUENCE_BEARER_TOKEN": secret})
	err := run(context.Background(), []string{"--sdk-stdio"}, strings.NewReader(requestJSON(t, server.URL)), &stdout, &stderr, env)
	if err != nil {
		t.Fatalf("run() error = %v; stderr = %s", err, stderr.String())
	}
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	var result sdk.Result
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("stdout is not one SDK result: %v", err)
	}
	if decoder.More() {
		t.Fatal("stdout carries more than one JSON document")
	}
	if result.State != sdk.ResultComplete {
		t.Fatalf("State = %q, want complete", result.State)
	}
	if _, err := sdk.NewValidator(confluence.Contract()).ValidateResult(result); err != nil {
		t.Fatalf("ValidateResult() error = %v", err)
	}
	if !strings.Contains(stderr.String(), "collector health:") {
		t.Errorf("stderr = %q, want the health line", stderr.String())
	}
	if strings.Contains(stderr.String(), secret) || strings.Contains(stdout.String(), secret) {
		t.Error("the credential reached an output stream")
	}
}

func TestRunStdioReportsMissingCredentialsAsAResult(t *testing.T) {
	t.Parallel()

	server := confluenceStub(t)
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"--sdk-stdio"}, strings.NewReader(requestJSON(t, server.URL)), &stdout, &stderr, getenv(nil))
	if err != nil {
		t.Fatalf("run() error = %v, want the host to receive a terminal result", err)
	}
	var result sdk.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not an SDK result: %v", err)
	}
	if result.State != sdk.ResultTerminal || result.Statuses[0].FailureClass != confluence.FailureMissingCredentials {
		t.Fatalf("result = %q %#v, want terminal missing-credentials", result.State, result.Statuses)
	}
	if !strings.Contains(stderr.String(), "ESHU_CONFLUENCE_API_TOKEN") {
		t.Errorf("stderr = %q, want the missing variable named", stderr.String())
	}
}

func TestRunRejectsBadInvocationsWithoutWritingAResult(t *testing.T) {
	t.Parallel()

	server := confluenceStub(t)
	env := getenv(map[string]string{"ESHU_CONFLUENCE_BEARER_TOKEN": secret})
	tests := map[string]struct {
		args  []string
		stdin string
	}{
		"no flag":          {args: nil, stdin: requestJSON(t, server.URL)},
		"unknown flag":     {args: []string{"--nope"}, stdin: ""},
		"empty stdin":      {args: []string{"--sdk-stdio"}, stdin: ""},
		"garbage stdin":    {args: []string{"--sdk-stdio"}, stdin: "{"},
		"wrong protocol":   {args: []string{"--sdk-stdio"}, stdin: strings.Replace(requestJSON(t, server.URL), "v1alpha1", "v9", 1)},
		"missing claim id": {args: []string{"--sdk-stdio"}, stdin: strings.Replace(requestJSON(t, server.URL), `"work_item_id":"component-work:abc"`, `"work_item_id":""`, 1)},
	}
	for name, tt := range tests {
		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), tt.args, strings.NewReader(tt.stdin), &stdout, &stderr, env); err == nil {
			t.Errorf("%s: run() error = nil, want a failure", name)
		}
		if stdout.Len() != 0 {
			t.Errorf("%s: stdout = %q, want nothing on a refused invocation", name, stdout.String())
		}
	}
}
