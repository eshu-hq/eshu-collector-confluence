// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// coreModulePrefix is the module path of the Eshu core Go module. The published
// SDK modules live under github.com/eshu-hq/eshu/sdk/go and are allowed; the
// core module lives under github.com/eshu-hq/eshu/go and is never allowed.
const coreModulePrefix = "github.com/eshu-hq/eshu/go"

// TestImportBoundaryRejectsCoreAndCrossCollectorImports is the CI gate that
// keeps a standalone collector repository standalone: no Eshu internal
// implementation imports and no cross-collector implementation imports. Only
// the published SDK modules plus the standard library and yaml are allowed.
// The self-import prefix is derived from go.mod so renaming the module can
// never desync this allowlist.
func TestImportBoundaryRejectsCoreAndCrossCollectorImports(t *testing.T) {
	t.Parallel()

	violations, err := importViolations(".", boundaryAllowedPrefixes(modulePath(t)))
	if err != nil {
		t.Fatalf("importViolations() error = %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("forbidden imports:\n%s", strings.Join(violations, "\n"))
	}
}

// TestGoModDoesNotRequireTheEshuCoreModule keeps the dependency edge out of
// go.mod too: an import check alone would pass for a require that a build tag
// or a later file starts to use.
func TestGoModDoesNotRequireTheEshuCoreModule(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("os.ReadFile(go.mod) error = %v", err)
	}
	if violations := goModViolations(string(raw)); len(violations) > 0 {
		t.Fatalf("go.mod breaks the boundary:\n%s", strings.Join(violations, "\n"))
	}
}

func boundaryAllowedPrefixes(self string) []string {
	return []string{
		self,
		"github.com/eshu-hq/eshu/sdk/go/collector",
		"github.com/eshu-hq/eshu/sdk/go/factschema",
		"golang.org/x/net/html",
		"gopkg.in/yaml.v3",
	}
}

// importViolations parses every .go file under root and reports each import
// that is not standard library, relative, or on the allowlist. Only the
// top-level testdata directory (recorded non-Go fixtures) and .git are skipped:
// a directory called scripts, compose, or testdata deeper in the tree can hold
// compiled code, so it is checked like any other.
func importViolations(root string, allowedPrefixes []string) ([]string, error) {
	violations := []string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			if rel == "testdata" || rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if importPath == "C" {
				violations = append(violations, path+": cgo import is not allowed")
				continue
			}
			if isStandardOrRelative(importPath) || hasAllowedPrefix(importPath, allowedPrefixes) {
				continue
			}
			violations = append(violations, path+": import "+importPath)
		}
		return nil
	})
	return violations, err
}

func hasAllowedPrefix(importPath string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if importPath == prefix || strings.HasPrefix(importPath, prefix+"/") {
			return true
		}
	}
	return false
}

// goModViolations reports go.mod lines that require a module under the Eshu
// core Go module path, or that redirect any module with a replace directive
// (a replace to a local checkout would hide a core dependency).
func goModViolations(goMod string) []string {
	violations := []string{}
	block := ""
	for _, raw := range strings.Split(goMod, "\n") {
		line := strings.TrimSpace(raw)
		if comment := strings.Index(line, "//"); comment >= 0 {
			line = strings.TrimSpace(line[:comment])
		}
		switch {
		case line == "":
			continue
		case strings.HasSuffix(line, "("):
			block = strings.TrimSpace(strings.TrimSuffix(line, "("))
			continue
		case line == ")":
			block = ""
			continue
		}
		directive, rest := block, line
		if block == "" {
			directive, rest, _ = strings.Cut(line, " ")
			rest = strings.TrimSpace(rest)
		}
		switch directive {
		case "require":
			modulePath, _, _ := strings.Cut(rest, " ")
			if modulePath == coreModulePrefix || strings.HasPrefix(modulePath, coreModulePrefix+"/") {
				violations = append(violations, "requires the Eshu core module: "+modulePath)
			}
		case "replace":
			violations = append(violations, "replace directive: "+rest)
		}
	}
	return violations
}

// modulePath reads the repository's own module path from go.mod so the
// self-import allowlist entry tracks renames automatically.
func modulePath(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("os.ReadFile(go.mod) error = %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			if strings.TrimSpace(path) == "" {
				break
			}
			return strings.TrimSpace(path)
		}
	}
	t.Fatal("go.mod declares no module path")
	return ""
}

func isStandardOrRelative(importPath string) bool {
	if strings.HasPrefix(importPath, ".") {
		return true
	}
	first, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(first, ".")
}
