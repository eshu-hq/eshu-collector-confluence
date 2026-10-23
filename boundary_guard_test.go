// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const badImportSource = "package x\n\nimport _ \"github.com/eshu-hq/eshu/go/internal/workflow\"\n"

// writeTree writes files (relative path to contents) under a fresh temp dir.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestImportBoundaryGuardCatchesABadImportAtAnyDepth proves the boundary check
// is not blind: a forbidden core import is reported in a root file and in
// directories named scripts, compose, and testdata below the top level, while
// only the top-level testdata fixture directory is skipped. The bad imports
// live in temporary trees, never in this repository.
func TestImportBoundaryGuardCatchesABadImportAtAnyDepth(t *testing.T) {
	t.Parallel()

	allowed := boundaryAllowedPrefixes("example.invalid/collector")
	for name, rel := range map[string]string{
		"root file":               "x.go",
		"nested scripts":          "internal/scripts/x.go",
		"nested compose":          "cmd/collector/compose/x.go",
		"nested testdata":         "internal/testdata/x.go",
		"top-level scripts":       "scripts/x.go",
		"deeply nested directory": "a/b/c/d/x.go",
	} {
		root := writeTree(t, map[string]string{rel: badImportSource})
		violations, err := importViolations(root, allowed)
		if err != nil {
			t.Fatalf("%s: importViolations() error = %v", name, err)
		}
		if len(violations) != 1 || !strings.Contains(violations[0], "github.com/eshu-hq/eshu/go/internal/workflow") {
			t.Errorf("%s: violations = %v, want the core import reported", name, violations)
		}
	}
}

func TestImportBoundaryGuardAcceptsAllowedImportsAndSkipsTopLevelFixtures(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]string{
		"ok.go": "package x\n\nimport (\n\t\"fmt\"\n\t_ \"example.invalid/collector/internal/httpx\"\n" +
			"\t_ \"github.com/eshu-hq/eshu/sdk/go/collector\"\n\t_ \"gopkg.in/yaml.v3\"\n)\n",
		"testdata/fixture.go": badImportSource,
	})
	violations, err := importViolations(root, boundaryAllowedPrefixes("example.invalid/collector"))
	if err != nil {
		t.Fatalf("importViolations() error = %v", err)
	}
	if len(violations) != 0 {
		t.Errorf("violations = %v, want none", violations)
	}
}

func TestGoModGuardRejectsCoreRequiresAndReplaces(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		goMod string
		want  int
	}{
		"clean": {
			goMod: "module m\n\nrequire (\n\tgithub.com/eshu-hq/eshu/sdk/go/collector v0.2.0\n\tgopkg.in/yaml.v3 v3.0.1 // indirect\n)\n",
			want:  0,
		},
		"core module in a block": {
			goMod: "module m\n\nrequire (\n\tgithub.com/eshu-hq/eshu/go v0.0.5\n)\n",
			want:  1,
		},
		"core subpath single line": {
			goMod: "module m\n\nrequire github.com/eshu-hq/eshu/go/internal/x v0.0.1\n",
			want:  1,
		},
		"core module marked indirect": {
			goMod: "module m\n\nrequire (\n\tgithub.com/eshu-hq/eshu/go v0.0.5 // indirect\n)\n",
			want:  1,
		},
		"replace to a local checkout": {
			goMod: "module m\n\nreplace github.com/eshu-hq/eshu/sdk/go/collector => ../eshu/sdk/go/collector\n",
			want:  1,
		},
		"replace in a block": {
			goMod: "module m\n\nreplace (\n\texample.invalid/a => ../a\n)\n",
			want:  1,
		},
		"sdk path that merely looks similar": {
			goMod: "module m\n\nrequire github.com/eshu-hq/eshu/sdk/go/factschema v0.2.0\n",
			want:  0,
		},
	}
	for name, tt := range tests {
		if got := goModViolations(tt.goMod); len(got) != tt.want {
			t.Errorf("%s: violations = %v, want %d", name, got, tt.want)
		}
	}
}
