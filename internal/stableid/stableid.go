// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package stableid

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Fact kinds the stable keys are derived for. They are the fact_type values
// the in-tree helpers hash, so they must not change.
const (
	sourceKind   = "documentation_source"
	documentKind = "documentation_document"
	sectionKind  = "documentation_section"
	linkKind     = "documentation_link"
)

// StableID returns a deterministic identifier for one fact-adjacent payload.
// It hashes the fact type together with the normalized identity map, so the
// same identity yields the same id on every run, host, and replay.
//
// TODO(eshu-hq/eshu#8851): replace with the published eshu-sdk helper.
func StableID(factType string, identity map[string]any) string {
	record := map[string]any{
		"fact_type": factType,
		"identity":  normalize(identity),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		panic(fmt.Sprintf("marshal stable id payload: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func normalize(value any) any {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, nested := range typed {
			cloned[key] = normalize(nested)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for i := range typed {
			cloned[i] = normalize(typed[i])
		}
		return cloned
	case []string:
		cloned := make([]any, len(typed))
		for i := range typed {
			cloned[i] = typed[i]
		}
		return cloned
	default:
		return typed
	}
}

// Source returns the stable key of a documentation_source fact.
func Source(sourceID, sourceSystem, externalID string) string {
	return StableID(sourceKind, map[string]any{
		"source_id":     sourceID,
		"source_system": sourceSystem,
		"external_id":   externalID,
	})
}

// Document returns the stable key of a documentation_document fact.
func Document(sourceID, documentID, externalID, revisionID string) string {
	return StableID(documentKind, map[string]any{
		"source_id":   sourceID,
		"document_id": documentID,
		"external_id": externalID,
		"revision_id": revisionID,
	})
}

// SectionIdentity carries the fields that identify one documentation section
// revision.
type SectionIdentity struct {
	DocumentID    string
	RevisionID    string
	SectionID     string
	SectionAnchor string
	OrdinalPath   []int
	TextHash      string
	ExcerptHash   string
}

// Section returns the stable key of a documentation_section fact.
func Section(id SectionIdentity) string {
	return StableID(sectionKind, map[string]any{
		"document_id":    id.DocumentID,
		"revision_id":    id.RevisionID,
		"section_id":     id.SectionID,
		"section_anchor": id.SectionAnchor,
		"ordinal_path":   id.OrdinalPath,
		"text_hash":      id.TextHash,
		"excerpt_hash":   id.ExcerptHash,
	})
}

// LinkIdentity carries the fields that identify one documentation link.
type LinkIdentity struct {
	DocumentID string
	RevisionID string
	SectionID  string
	LinkID     string
	TargetURI  string
}

// Link returns the stable key of a documentation_link fact.
func Link(id LinkIdentity) string {
	return StableID(linkKind, map[string]any{
		"document_id": id.DocumentID,
		"revision_id": id.RevisionID,
		"section_id":  id.SectionID,
		"link_id":     id.LinkID,
		"target_uri":  id.TargetURI,
	})
}
