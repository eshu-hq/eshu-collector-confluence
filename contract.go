// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

const (
	// ComponentID is the component manifest identity.
	ComponentID = "dev.eshu.collector.confluence"
	// CollectorKind is the collector family the activation is planned under.
	CollectorKind = "documentation"
	// SourceSystem names the observed source, matching the in-tree collector.
	SourceSystem = "confluence"
	// MetricsPrefix is the component-owned metric prefix from the manifest.
	MetricsPrefix = "eshu_dp_confluence_collector_"
	// ExtractorVersion folds into the freshness hint so an extraction change
	// stores a fresh generation instead of being skipped as a duplicate of the
	// previous one. Bump it whenever the emitted facts change for the same
	// Confluence content.
	ExtractorVersion = "1"
)

// Documentation fact kinds and schema versions this collector emits. The
// values are the source-neutral kinds the Git documentation path also emits,
// so core consumers are unchanged.
const (
	// FactKindSource is one documentation source (a space or page tree).
	FactKindSource = "documentation_source"
	// FactKindDocument is one page revision.
	FactKindDocument = "documentation_document"
	// FactKindSection is one section of a page revision.
	FactKindSection = "documentation_section"
	// FactKindLink is one outbound link found in a section.
	FactKindLink = "documentation_link"

	// SchemaVersion is the schema version of source, document, and link.
	SchemaVersion = "1.0.0"
	// SectionSchemaVersion is the schema version of section facts.
	SectionSchemaVersion = "1.1.0"
)

// Contract returns the SDK fact families this collector may emit.
func Contract() sdk.Contract {
	observed := []sdk.SourceConfidence{sdk.SourceConfidenceObserved}
	return sdk.Contract{
		ProtocolVersion: sdk.ProtocolVersionV1Alpha1,
		Facts: []sdk.FactDeclaration{
			{Kind: FactKindSource, SchemaVersions: []string{SchemaVersion}, SourceConfidence: observed},
			{Kind: FactKindDocument, SchemaVersions: []string{SchemaVersion}, SourceConfidence: observed},
			{Kind: FactKindSection, SchemaVersions: []string{SectionSchemaVersion}, SourceConfidence: observed},
			{Kind: FactKindLink, SchemaVersions: []string{SchemaVersion}, SourceConfidence: observed},
		},
	}
}

// Bounded failure classes carried on SDK failure statuses. Each one is a short
// static token: never a URL, page id, title, body, or credential.
const (
	// FailureInvalidConfig marks an activation config the collector rejects.
	FailureInvalidConfig = "invalid-config"
	// FailureMissingCredentials marks absent or incomplete credentials.
	FailureMissingCredentials = "missing-credentials"
	// FailureAuthFailed marks a rejected credential (HTTP 401).
	FailureAuthFailed = "auth-failed"
	// FailurePermissionDenied marks a space or page tree the credential cannot read.
	FailurePermissionDenied = "permission-denied"
	// FailureRateLimited marks a provider throttle (HTTP 429).
	FailureRateLimited = "rate-limited"
	// FailureProviderUnavailable marks a retryable provider outage (HTTP 5xx).
	FailureProviderUnavailable = "provider-unavailable"
	// FailureTransport marks a transient connection-level failure.
	FailureTransport = "transport-error"
	// FailureTransportPersistent marks a transport failure that kept repeating.
	FailureTransportPersistent = "transport-persistent"
	// FailureConnectionFailed marks a non-transient connection failure such as
	// a TLS or DNS-not-found error.
	FailureConnectionFailed = "connection-failed"
	// FailureProviderRejected marks a non-retryable provider status.
	FailureProviderRejected = "provider-rejected"
	// FailureInvalidResponse marks a response the collector cannot decode.
	FailureInvalidResponse = "invalid-response"
	// FailureResponseTooLarge marks a response over the configured byte cap.
	FailureResponseTooLarge = "response-too-large"
	// FailureUntrustedLink marks a pagination link that leaves the base URL.
	FailureUntrustedLink = "untrusted-next-link"
	// FailureDeadline marks a claim whose deadline passed before completion.
	FailureDeadline = "claim-deadline-exceeded"
	// FailureInvalidClaim marks a claim the collector cannot serve.
	FailureInvalidClaim = "invalid-claim"
	// FailureInternal marks a fact-build failure inside the collector.
	FailureInternal = "internal-error"
	// WarningPagesSkipped marks pages left out of a partial snapshot.
	WarningPagesSkipped = "pages-skipped"
)
