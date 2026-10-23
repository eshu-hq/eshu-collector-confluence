// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

// maxRequestBytes bounds the request document read from stdin. A host request
// is a claim plus a small config, so anything larger is not a host request.
const maxRequestBytes = 1 << 20

// Request is the bounded JSON document the host sends on stdin: one claim, the
// host's fact contract, and the activation config.
type Request struct {
	ProtocolVersion string         `json:"protocol_version"`
	Claim           sdk.Claim      `json:"claim"`
	Contract        sdk.Contract   `json:"contract"`
	Config          map[string]any `json:"config,omitempty"`
}

// DecodeRequest reads exactly one request document from r.
func DecodeRequest(r io.Reader) (Request, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxRequestBytes+1))
	if err != nil {
		return Request{}, fmt.Errorf("read SDK request: %w", err)
	}
	if len(raw) > maxRequestBytes {
		return Request{}, fmt.Errorf("SDK request exceeds %d bytes", maxRequestBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, fmt.Errorf("decode SDK request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Request{}, errors.New("decode SDK request: trailing JSON value")
	}
	return request, nil
}

// HandleOptions are the collaborators Handle uses; the zero value selects the
// real environment, network, and clock.
type HandleOptions struct {
	// Getenv resolves credential environment variables. It is required: Handle
	// never reads the process environment itself, so callers pass os.Getenv.
	Getenv func(string) string
	// HTTP is the transport for Confluence reads; nil selects the default.
	HTTP *http.Client
	// Clock returns the current time; nil selects time.Now.
	Clock func() time.Time
	// Client replaces the HTTP client entirely, for tests.
	Client Client
}

// Handled is one served request: the result and what operators need to read.
type Handled struct {
	// Result is the result to write to stdout.
	Result sdk.Result
	// Requests counts provider requests by operation and outcome.
	Requests map[string]int64
	// Detail is a short, credential-free reason for a failure result, for the
	// stderr health line. Empty on success.
	Detail string
}

// Handle serves one request. A request the collector cannot answer at all (an
// unsupported protocol or an incomplete claim) returns an error; every other
// outcome, including a bad activation config, is a result the host records.
func Handle(ctx context.Context, request Request, options HandleOptions) (Handled, error) {
	if strings.TrimSpace(request.ProtocolVersion) != sdk.ProtocolVersionV1Alpha1 {
		return Handled{}, fmt.Errorf("protocol_version %q is unsupported", request.ProtocolVersion)
	}
	if err := validateClaim(request.Claim); err != nil {
		return Handled{}, err
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	config, err := ParseConfig(request.Config, options.Getenv)
	if err != nil {
		class := ClassifyConfigError(err)
		return Handled{
			Result:   terminalResult(request.Claim, clock().UTC(), class),
			Requests: map[string]int64{},
			Detail:   err.Error(),
		}, nil
	}
	collector := &Collector{Config: config, Client: options.Client, HTTP: options.HTTP, Clock: clock}
	result := collector.Collect(ctx, request.Claim)
	return Handled{Result: result, Requests: collector.Requests(), Detail: failureDetail(result)}, nil
}

func failureDetail(result sdk.Result) string {
	for _, status := range result.Statuses {
		if status.Class == sdk.StatusFailure {
			return status.FailureClass
		}
	}
	return ""
}

func validateClaim(claim sdk.Claim) error {
	required := map[string]string{
		"claim.component_id":   claim.ComponentID,
		"claim.instance_id":    claim.InstanceID,
		"claim.collector_kind": claim.CollectorKind,
		"claim.source_system":  claim.SourceSystem,
		"claim.scope.id":       claim.Scope.ID,
		"claim.scope.kind":     claim.Scope.Kind,
		"claim.source_run_id":  claim.SourceRunID,
		"claim.generation_id":  claim.GenerationID,
		"claim.work_item_id":   claim.WorkItemID,
		"claim.fencing_token":  claim.FencingToken,
		"claim.config_handle":  claim.ConfigHandle,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", field)
		}
	}
	if claim.Attempt < 1 {
		return errors.New("claim.attempt must be >= 1")
	}
	if claim.Deadline.IsZero() {
		return errors.New("claim.deadline is required")
	}
	return nil
}
