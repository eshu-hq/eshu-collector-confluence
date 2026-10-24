// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package stableid

import (
	"testing"
	"time"
)

// The golden values below were computed from the in-tree functions
// (docs.SourceStableID, docs.DocumentStableID, docs.SectionStableID,
// docs.LinkStableID, facts.StableID) at eshu-hq/eshu commit ce050339cf. They
// pin byte equivalence with the Eshu core documentation path.
const source = "doc-source:confluence:confluence.example.invalid:12345"

func TestStableKeysMatchInTreeGoldens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"source space", Source(source, "confluence", "12345"), "479bc25b9f7b65aa087d95cecd091ffeebff5bc2c40f16bd8d830ca815684e73"},
		{"source page tree", Source(source, "confluence", "98765"), "bb3aa091f244bcd3a28820c9c64c217be3f6e9f5bfaf6694d208f98afadfaeb4"},
		{"document", Document(source, "doc:confluence:1001", "1001", "3"), "cb84504fe61ff3d83c54f04caab663d9e8d3a43857cfdab412e3bb22517ceaec"},
		{"section", Section(SectionIdentity{
			DocumentID: "doc:confluence:1001", RevisionID: "3", SectionID: "body", SectionAnchor: "body",
			OrdinalPath: []int{1}, TextHash: "sha256:aa", ExcerptHash: "sha256:bb",
		}), "357c25ac2af4afc390157c1113482e49b3c00b213d0d6e4d937ac89c7e1c863e"},
		{"section nested ordinal and empty excerpt", Section(SectionIdentity{
			DocumentID: "doc:confluence:1001", RevisionID: "3", SectionID: "s2", SectionAnchor: "a2",
			OrdinalPath: []int{1, 2}, TextHash: "sha256:cc",
		}), "51887521cfca9053f32a2c0b29e172efc23f33271d749e70cdf997b3b5176166"},
		{"link external", Link(LinkIdentity{
			DocumentID: "doc:confluence:1001", RevisionID: "3", SectionID: "body", LinkID: "link:1",
			TargetURI: "https://docs.example.invalid/a?b=c",
		}), "e2a3bce868cad2de9705ef19c0e2f458b7c903309f62c0fa9569756b6367ac62"},
		{"link reference", Link(LinkIdentity{
			DocumentID: "doc:confluence:1001", RevisionID: "3", SectionID: "body", LinkID: "link:2",
			TargetURI: "confluence:page:2002",
		}), "8db2de552dfa110587f4130059b97986a1709cecea6f65ad9c99fe3e33c6d7a6"},
		{"generic mixed identity", StableID("ParityProbe", map[string]any{
			"when":   time.Date(2026, 10, 1, 12, 30, 0, 123456789, time.FixedZone("x", 3600)),
			"nested": map[string]any{"k": []any{"a", 1, true, map[string]any{"z": nil}}},
			"strs":   []string{"b", "a"},
			"int":    42,
			"uni":    "café ☃ <&>",
			"empty":  "",
		}), "11003bd2256f500f2fe413ea183e51877152eeacfafa80afb3511cde707b9051"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %s, want %s", tt.name, tt.got, tt.want)
		}
	}
}

func TestStableKeyChangesWithIdentity(t *testing.T) {
	t.Parallel()

	base := Document(source, "doc:confluence:1001", "1001", "3")
	if Document(source, "doc:confluence:1001", "1001", "4") == base {
		t.Fatal("document stable key must change with the revision")
	}
	if Document(source+"x", "doc:confluence:1001", "1001", "3") == base {
		t.Fatal("document stable key must change with the source")
	}
	if Document(source, "doc:confluence:1001", "1001", "3") != base {
		t.Fatal("document stable key must be deterministic")
	}
}
