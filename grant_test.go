// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// GrantRequest is the shape of a core-issued producer grant (`eshu component
// grant`). The core issues and enforces the grant; this collector stores only
// metadata and never mints canonical identity. A revoked grant never matches
// and a zero expiry is already expired, mirroring the host.
type GrantRequest struct {
	ProducerID     string    `json:"producer_id"`
	Version        string    `json:"version"`
	Kind           string    `json:"kind"`
	SchemaVersions []string  `json:"schema_versions"`
	Scope          string    `json:"scope"`
	ExpiresAt      time.Time `json:"expires_at"`
	Revoked        bool      `json:"revoked"`
}

func loadGrants(t *testing.T) []GrantRequest {
	t.Helper()
	raw, err := os.ReadFile("grant.example.json")
	if err != nil {
		t.Fatalf("os.ReadFile(grant.example.json) error = %v", err)
	}
	var grants []GrantRequest
	if err := json.Unmarshal(raw, &grants); err != nil {
		t.Fatalf("json.Unmarshal(grant.example.json) error = %v", err)
	}
	return grants
}

// TestGrantExampleCoversEveryEmittedKind proves the checked-in example grants
// exactly the four core kinds and schema versions this collector emits, for
// this producer and version, scoped to the manifest's collector kind. The host
// binds a grant to the collector kind, not to one source scope, so a single
// grant set covers every activation of the component.
func TestGrantExampleCoversEveryEmittedKind(t *testing.T) {
	t.Parallel()

	grants := loadGrants(t)
	manifest := loadManifest(t)
	if len(grants) != len(manifest.Spec.EmittedFacts) {
		t.Fatalf("grants = %d, want one per emitted kind (%d)", len(grants), len(manifest.Spec.EmittedFacts))
	}
	for _, family := range manifest.Spec.EmittedFacts {
		found := false
		for _, grant := range grants {
			if grant.Kind == family.Kind {
				found = true
				if strings.Join(grant.SchemaVersions, ",") != strings.Join(family.SchemaVersions, ",") {
					t.Errorf("%s grant schema versions = %v, want %v", family.Kind, grant.SchemaVersions, family.SchemaVersions)
				}
			}
		}
		if !found {
			t.Errorf("no grant for %s", family.Kind)
		}
	}
	for _, grant := range grants {
		if grant.ProducerID != ComponentID || grant.Version != manifest.Metadata.Version || grant.Scope != CollectorKind || grant.Revoked {
			t.Errorf("grant = %+v, want producer %s version %s scope %s, not revoked", grant, ComponentID, manifest.Metadata.Version, CollectorKind)
		}
	}
}

// TestProducerGrantContract proves every fact a complete result emits is
// authorized by the example grants, and that each way a grant can fail closes
// the emission.
func TestProducerGrantContract(t *testing.T) {
	t.Parallel()

	now := testNow()
	grants := loadGrants(t)
	manifest := loadManifest(t)
	scopes := manifest.Spec.CollectorKinds
	version := manifest.Metadata.Version

	complete := loadConformanceFixture(t, "complete")
	for _, fact := range complete.Facts {
		if !authorizesEmission(grants, ComponentID, version, fact.Kind, fact.SchemaVersion, scopes, now) {
			t.Fatalf("%s %s is not authorized by grant.example.json", fact.Kind, fact.SchemaVersion)
		}
	}

	missing := grants[1:] // drops the documentation_source grant
	if authorizesEmission(missing, ComponentID, version, FactKindSource, SchemaVersion, scopes, now) {
		t.Error("a kind without a grant must fail closed")
	}
	if !authorizesEmission(missing, ComponentID, version, FactKindDocument, SchemaVersion, scopes, now) {
		t.Error("dropping one grant must not affect the other kinds")
	}

	mutations := map[string]func(*GrantRequest){
		"revoked":         func(g *GrantRequest) { g.Revoked = true },
		"expired":         func(g *GrantRequest) { g.ExpiresAt = now.Add(-time.Minute) },
		"zero expiry":     func(g *GrantRequest) { g.ExpiresAt = time.Time{} },
		"other producer":  func(g *GrantRequest) { g.ProducerID = "dev.eshu.other.collector" },
		"other version":   func(g *GrantRequest) { g.Version = "0.2.0" },
		"other scope":     func(g *GrantRequest) { g.Scope = "jira" },
		"other schema":    func(g *GrantRequest) { g.SchemaVersions = []string{"9.9.9"} },
		"padded producer": func(g *GrantRequest) { g.ProducerID = " " + g.ProducerID },
	}
	for name, mutate := range mutations {
		mutated := append([]GrantRequest(nil), grants...)
		mutate(&mutated[0])
		if authorizesEmission(mutated, ComponentID, version, FactKindSource, SchemaVersion, scopes, now) {
			t.Errorf("%s grant must fail closed", name)
		}
	}
	if authorizesEmission(grants, ComponentID, version, FactKindSection, SchemaVersion, scopes, now) {
		t.Error("section 1.0.0 is not granted; only 1.1.0 is")
	}
}

// TestEmissionStaysInsideTheClaimScope proves no fact names a scope other than
// the claimed one, so conflicting producers never share a scope and generation.
func TestEmissionStaysInsideTheClaimScope(t *testing.T) {
	t.Parallel()

	complete := loadConformanceFixture(t, "complete")
	for _, fact := range complete.Facts {
		if fact.SourceRef.ScopeID != complete.Claim.Scope.ID {
			t.Errorf("%s names scope %q, outside the claim scope %q", fact.Kind, fact.SourceRef.ScopeID, complete.Claim.Scope.ID)
		}
	}
}

// authorizesEmission mirrors go/internal/component.AuthorizesEmission without
// importing it: core-owned (unprefixed) kinds require a live grant that names
// this producer and version, covers the schema version, and is scoped to one of
// the manifest's collector kinds; namespaced kinds pass. Core remains the
// enforcement owner; this keeps the pre-submit check identical to its verdict.
func authorizesEmission(grants []GrantRequest, producerID, version, kind, schemaVersion string, collectorKinds []string, now time.Time) bool {
	kind = strings.TrimSpace(kind)
	if strings.Contains(kind, ".") {
		return true
	}
	declared := map[string]bool{}
	for _, collectorKind := range collectorKinds {
		if strings.TrimSpace(collectorKind) != "" {
			declared[collectorKind] = true
		}
	}
	for _, grant := range grants {
		if grant.Revoked || !now.Before(grant.ExpiresAt) {
			continue
		}
		if grant.ProducerID != producerID || normalizeVersion(grant.Version) != normalizeVersion(version) {
			continue
		}
		if !declared[grant.Scope] || grant.Kind != kind {
			continue
		}
		for _, covered := range grant.SchemaVersions {
			if covered == schemaVersion {
				return true
			}
		}
	}
	return false
}

func normalizeVersion(version string) string {
	return strings.TrimPrefix(strings.TrimSpace(version), "v")
}
