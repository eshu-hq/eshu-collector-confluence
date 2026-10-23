// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"encoding/json"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

// Outcome is one claim's result. It embeds the published SDK result and adds
// the one wire field the published SDK module does not yet carry.
//
// The empty-successor attestation (generation.trusted_deletion) is part of the
// host's result decoder on current core but is not a field of sdk/go/collector
// v0.2.0's Generation. Outcome.MarshalJSON adds it to the generation object on
// the wire, and only when the operator attested it, so a result without the
// attestation is byte-identical to a plain sdk.Result and still decodes on a
// host built from v0.2.0.
//
// TODO(eshu-hq/eshu#8851): drop this overlay and set Generation.TrustedDeletion
// directly once an SDK release carries the field.
type Outcome struct {
	sdk.Result
	// TrustedDeletion is the operator's attestation that an empty or sharply
	// shrunken successor generation is legitimate for this scope.
	TrustedDeletion bool
}

// wireGeneration and wireResult spell out sdk.Generation and sdk.Result field
// by field, in the SDK's order, so an unattested result is byte-identical to a
// plain sdk.Result. outcome_test.go fails when a new SDK release adds a field
// these copies lack.
type wireGeneration struct {
	ID              string    `json:"id"`
	ObservedAt      time.Time `json:"observed_at"`
	FreshnessHint   string    `json:"freshness_hint,omitempty"`
	TrustedDeletion bool      `json:"trusted_deletion,omitempty"`
}

type wireResult struct {
	ProtocolVersion string          `json:"protocol_version"`
	State           sdk.ResultState `json:"state"`
	Claim           sdk.Claim       `json:"claim"`
	Generation      wireGeneration  `json:"generation"`
	Facts           []sdk.Fact      `json:"facts,omitempty"`
	Statuses        []sdk.Status    `json:"statuses,omitempty"`
}

// MarshalJSON encodes the result in collector-sdk/v1alpha1 wire form.
func (o Outcome) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireResult{
		ProtocolVersion: o.Result.ProtocolVersion,
		State:           o.Result.State,
		Claim:           o.Result.Claim,
		Generation: wireGeneration{
			ID:              o.Result.Generation.ID,
			ObservedAt:      o.Result.Generation.ObservedAt,
			FreshnessHint:   o.Result.Generation.FreshnessHint,
			TrustedDeletion: o.TrustedDeletion,
		},
		Facts:    o.Result.Facts,
		Statuses: o.Result.Statuses,
	})
}
