// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"encoding/json"
	"fmt"

	"github.com/eshu-hq/eshu/sdk/go/factschema"
	documentationv1 "github.com/eshu-hq/eshu/sdk/go/factschema/documentation/v1"
)

// Source ACL vocabulary, matching the bounded source_acl_state values the
// in-tree collector emits. Confluence reads are credential-viewable but the
// per-source restriction set is not collected, so the ACL read is incomplete
// and stays partial: fail closed, never upgraded to allowed.
const (
	aclStatePartial       = "partial"
	aclVisibility         = "credential_viewable"
	sourceACLPartialCause = "confluence_source_restrictions_not_collected"
	pageACLPartialCause   = "confluence_page_restrictions_not_collected"
)

func strPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func boolPtr(value bool) *bool {
	if !value {
		return nil
	}
	return &value
}

func partialACL(cause string) *documentationv1.ACLSummary {
	return &documentationv1.ACLSummary{
		Visibility:     strPtr(aclVisibility),
		IsPartial:      boolPtr(true),
		SourceACLState: strPtr(aclStatePartial),
		PartialReason:  strPtr(cause),
	}
}

// jsonShape returns the payload as a JSON round trip produces it, so the map
// the SDK validator sees equals the map the host decodes off stdout.
func jsonShape(payload map[string]any, err error) (map[string]any, error) {
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	var shaped map[string]any
	if err := json.Unmarshal(raw, &shaped); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	return shaped, nil
}

func encodeSource(payload documentationv1.Source) (map[string]any, error) {
	return jsonShape(factschema.EncodeDocumentationSource(payload))
}

func encodeDocument(payload documentationv1.Document) (map[string]any, error) {
	return jsonShape(factschema.EncodeDocumentationDocument(payload))
}

func encodeSection(payload documentationv1.Section) (map[string]any, error) {
	return jsonShape(factschema.EncodeDocumentationSection(payload))
}

func encodeLink(payload documentationv1.Link) (map[string]any, error) {
	return jsonShape(factschema.EncodeDocumentationLink(payload))
}
