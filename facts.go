// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
	documentationv1 "github.com/eshu-hq/eshu/sdk/go/factschema/documentation/v1"

	"github.com/eshu-hq/eshu-collector-confluence/internal/stableid"
)

const (
	// resultOverheadBytes reserves room in the result budget for the claim,
	// generation, and status envelope around the facts.
	resultOverheadBytes = 16 << 10
	// sourceReserveBytes reserves room for the documentation_source fact, which
	// is built last because its metadata counts what the pages produced.
	sourceReserveBytes = 8 << 10
)

// factBuilder turns one snapshot into SDK facts for one claim.
type factBuilder struct {
	claim      sdk.Claim
	cfg        Config
	observedAt time.Time
}

// pageGroup is every fact one page produces and their encoded size.
type pageGroup struct {
	facts []sdk.Fact
	bytes int
}

// assembled is the fact set for one claim and what was left out of it.
type assembled struct {
	facts []sdk.Fact
	// pageCount is the number of pages whose facts are in the result.
	pageCount int
	// skipped counts pages left out: unreadable, oversize, or over budget.
	skipped int
	// truncated reports the cursor walk or the result budget cut the page set.
	truncated bool
}

func (b factBuilder) documentID(page Page) string { return "doc:confluence:" + page.ID }

// assemble builds the source fact and every page's facts within the result
// byte budget. Pages are visited in id order and a page is included whole or
// not at all: the source fact's counts always describe the included pages.
func (b factBuilder) assemble(ctx context.Context, snap snapshot) (assembled, error) {
	out := assembled{skipped: snap.skipped, truncated: snap.truncated}
	budget := b.cfg.MaxResultBytes - resultOverheadBytes - sourceReserveBytes
	used := 0
	var pageFacts []sdk.Fact
	for _, page := range snap.pages {
		if err := ctx.Err(); err != nil {
			return assembled{}, err
		}
		if len(page.Body.Storage.Value) > b.cfg.MaxPageBytes {
			out.skipped++
			continue
		}
		group, err := b.pageFacts(page)
		if err != nil {
			return assembled{}, err
		}
		if used+group.bytes > budget {
			// Skip this page and keep trying smaller later ones: the choice is
			// deterministic because pages are visited in id order.
			out.skipped++
			out.truncated = true
			continue
		}
		used += group.bytes
		pageFacts = append(pageFacts, group.facts...)
		out.pageCount++
	}
	source, err := b.sourceFact(snap.space, out)
	if err != nil {
		return assembled{}, err
	}
	out.facts = append([]sdk.Fact{source}, pageFacts...)
	return out, nil
}

func (b factBuilder) sourceID() string { return b.claim.Scope.ID }

func (b factBuilder) sourceFact(space Space, state assembled) (sdk.Fact, error) {
	externalID := firstNonEmpty(space.ID, b.cfg.SpaceID, b.cfg.RootPageID)
	payload, err := encodeSource(documentationv1.Source{
		SourceID:     b.sourceID(),
		SourceSystem: SourceSystem,
		ExternalID:   externalID,
		DisplayName:  strPtr(firstNonEmpty(space.Name, space.Key, b.cfg.SpaceID, b.cfg.RootPageID)),
		BaseURI:      strPtr(b.cfg.BaseURL),
		SourceType:   strPtr(b.sourceType()),
		Labels:       nonEmptyStrings(firstNonEmpty(space.Key, b.cfg.SpaceKey)),
		ACLSummary:   partialACL(sourceACLPartialCause),
		SourceMetadata: map[string]string{
			"page_count":       strconv.Itoa(state.pageCount),
			"failure_count":    strconv.Itoa(state.skipped),
			"sync_status":      syncStatus(state.skipped),
			"coverage_warning": coverageWarning(state.truncated),
			"max_total_pages":  strconv.Itoa(normalizedMaxTotalPages(b.cfg.MaxTotalPages)),
		},
	})
	if err != nil {
		return sdk.Fact{}, fmt.Errorf("encode documentation_source: %w", err)
	}
	key := stableid.Source(b.sourceID(), SourceSystem, externalID)
	return b.fact(FactKindSource, SchemaVersion, key, payload, b.cfg.BaseURL, externalID), nil
}

func (b factBuilder) sourceType() string {
	if b.cfg.SpaceID != "" {
		return "confluence_space"
	}
	return "confluence_page_tree"
}

// pageFacts builds the document, section, and link facts for one page.
func (b factBuilder) pageFacts(page Page) (pageGroup, error) {
	body := page.Body.Storage.Value
	format := firstNonEmpty(page.Body.Storage.Representation, "storage")
	revision := strconv.Itoa(page.Version.Number)
	documentID := b.documentID(page)
	uri := canonicalURI(b.cfg.BaseURL, page)

	var facts []sdk.Fact
	documentPayload, err := encodeDocument(documentv1(b.sourceID(), documentID, revision, format, uri, page))
	if err != nil {
		return pageGroup{}, fmt.Errorf("encode documentation_document: %w", err)
	}
	documentKey := stableid.Document(b.sourceID(), documentID, page.ID, revision)
	facts = append(facts, b.fact(FactKindDocument, SchemaVersion, documentKey, documentPayload, uri, page.ID))

	textHash, excerptHash := hashText(body), hashText(plainText(body))
	section := documentationv1.Section{
		DocumentID:     documentID,
		RevisionID:     revision,
		SectionID:      "body",
		SectionAnchor:  strPtr("body"),
		HeadingText:    strPtr(page.Title),
		OrdinalPath:    []int{1},
		Content:        strPtr(body),
		ContentFormat:  strPtr(format),
		TextHash:       strPtr(textHash),
		ExcerptHash:    strPtr(excerptHash),
		SourceStartRef: strPtr("storage:body"),
		SourceEndRef:   strPtr("storage:body"),
		SourceMetadata: map[string]string{"source_format": format},
	}
	sectionPayload, err := encodeSection(section)
	if err != nil {
		return pageGroup{}, fmt.Errorf("encode documentation_section: %w", err)
	}
	sectionKey := stableid.Section(stableid.SectionIdentity{
		DocumentID: documentID, RevisionID: revision, SectionID: "body", SectionAnchor: "body",
		OrdinalPath: []int{1}, TextHash: textHash, ExcerptHash: excerptHash,
	})
	facts = append(facts, b.fact(FactKindSection, SectionSchemaVersion, sectionKey, sectionPayload, uri, page.ID))

	for index, link := range extractLinks(body) {
		linkID := "link:" + strconv.Itoa(index+1)
		linkPayload, err := encodeLink(documentationv1.Link{
			DocumentID:     documentID,
			RevisionID:     strPtr(revision),
			SectionID:      strPtr("body"),
			LinkID:         linkID,
			TargetURI:      link.href,
			TargetKind:     strPtr(targetKind(link.href)),
			AnchorTextHash: strPtr(hashText(link.text)),
		})
		if err != nil {
			return pageGroup{}, fmt.Errorf("encode documentation_link: %w", err)
		}
		linkKey := stableid.Link(stableid.LinkIdentity{
			DocumentID: documentID, RevisionID: revision, SectionID: "body", LinkID: linkID, TargetURI: link.href,
		})
		facts = append(facts, b.fact(FactKindLink, SchemaVersion, linkKey, linkPayload, uri, page.ID))
	}
	size, err := encodedSize(facts)
	if err != nil {
		return pageGroup{}, err
	}
	return pageGroup{facts: facts, bytes: size}, nil
}

func documentv1(sourceID, documentID, revision, format, uri string, page Page) documentationv1.Document {
	var owners []documentationv1.OwnerRef
	if owner := firstNonEmpty(page.OwnerID, page.AuthorID); owner != "" {
		owners = []documentationv1.OwnerRef{{Kind: strPtr("confluence_user"), ID: strPtr(owner)}}
	}
	return documentationv1.Document{
		DocumentID:       documentID,
		SourceID:         strPtr(sourceID),
		ExternalID:       strPtr(page.ID),
		RevisionID:       strPtr(revision),
		CanonicalURI:     strPtr(uri),
		Title:            strPtr(page.Title),
		ParentDocumentID: strPtr(parentDocumentID(page.ParentID)),
		DocumentType:     strPtr("page"),
		Format:           strPtr(format),
		Labels:           labelNames(page.Labels.Results),
		OwnerRefs:        owners,
		ACLSummary:       partialACL(pageACLPartialCause),
		SourceMetadata: map[string]string{
			"space_id":  page.SpaceID,
			"status":    page.Status,
			"owner_id":  page.OwnerID,
			"author_id": page.AuthorID,
		},
		ContentHash:       strPtr(hashText(page.Body.Storage.Value)),
		DocumentUpdatedAt: strPtr(page.Version.CreatedAt),
	}
}

// fact wraps one payload in an SDK fact bound to the claim's scope and
// generation.
func (b factBuilder) fact(kind, schemaVersion, key string, payload map[string]any, uri, recordID string) sdk.Fact {
	return sdk.Fact{
		Kind:             kind,
		SchemaVersion:    schemaVersion,
		StableKey:        key,
		SourceConfidence: sdk.SourceConfidenceObserved,
		ObservedAt:       b.observedAt,
		SourceRef: sdk.SourceRef{
			SourceSystem: b.claim.SourceSystem,
			ScopeID:      b.claim.Scope.ID,
			GenerationID: b.claim.GenerationID,
			FactKey:      key,
			URI:          uri,
			RecordID:     recordID,
		},
		Payload: payload,
	}
}

func encodedSize(facts []sdk.Fact) (int, error) {
	total := 0
	for _, fact := range facts {
		raw, err := json.Marshal(fact)
		if err != nil {
			return 0, fmt.Errorf("encode %s fact: %w", fact.Kind, err)
		}
		total += len(raw) + 1
	}
	return total, nil
}

func syncStatus(skipped int) string {
	if skipped > 0 {
		return "partial"
	}
	return "complete"
}

// Coverage warning values on the source fact, matching the in-tree collector.
const (
	coverageComplete  = "complete"
	coverageTruncated = "truncated"
)

func coverageWarning(truncated bool) string {
	if truncated {
		return coverageTruncated
	}
	return coverageComplete
}

// freshnessHint is an exact identity for the content of one fact set. The core
// store skips a snapshot whose hint equals the active generation's, so the
// hint must change whenever any emitted kind, stable key, or payload changes.
// ExtractorVersion and the declared schema versions are folded in so a new
// collector release stores a fresh generation. Timestamps and the generation
// id are excluded: they differ on every claim and carry no content.
func freshnessHint(facts []sdk.Fact) string {
	type entry struct{ kind, key, payload string }
	entries := make([]entry, 0, len(facts))
	for _, fact := range facts {
		raw, err := json.Marshal(fact.Payload)
		if err != nil {
			raw = nil
		}
		entries = append(entries, entry{fact.Kind, fact.StableKey, string(raw)})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].kind != entries[j].kind {
			return entries[i].kind < entries[j].kind
		}
		return entries[i].key < entries[j].key
	})
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "confluence-extractor:%s\x00%s\x00%s\x00", ExtractorVersion, SchemaVersion, SectionSchemaVersion)
	for _, item := range entries {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x01", item.kind, item.key, item.payload)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
