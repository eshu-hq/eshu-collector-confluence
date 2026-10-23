// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// tagCheckScript returns the run script of the release workflow's tag check, so
// the test executes the shell the workflow runs.
func tagCheckScript(t *testing.T, raw []byte) string {
	t.Helper()
	var doc workflowDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("release.yml is not valid YAML: %v", err)
	}
	for _, step := range doc.Jobs["verify"].Steps {
		if step.Name == tagStepName {
			return step.Run
		}
	}
	t.Fatalf("release.yml has no %q step", tagStepName)
	return ""
}

// runTagCheck runs the tag-check script in a directory holding the given
// manifest text, with TAG set as the workflow's env does, and reports whether
// it exited zero.
func runTagCheck(t *testing.T, script, manifest, tag string) bool {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("os.WriteFile(manifest.yaml) error = %v", err)
	}
	cmd := exec.Command("bash", "-eo", "pipefail", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TAG="+tag)
	return cmd.Run() == nil
}

func TestReleaseTagCheckComparesTheTagWithTheManifestVersion(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(release.yml) error = %v", err)
	}
	script := tagCheckScript(t, raw)
	const manifest = "apiVersion: eshu.dev/v1alpha1\nmetadata:\n  id: x\n  version: 0.1.0\nspec:\n  version: 9.9.9\n"

	tests := []struct {
		name     string
		manifest string
		tag      string
		want     bool
	}{
		{"exact", manifest, "v0.1.0", true},
		{"pre-release suffix", manifest, "v0.1.0-rc1", true},
		{"dotted pre-release suffix", manifest, "v0.1.0-beta.2", true},
		{"newer patch", manifest, "v0.1.1", false},
		{"older", manifest, "v0.0.9", false},
		{"missing v", manifest, "0.1.0", false},
		{"extra segment", manifest, "v0.1.0.1", false},
		{"longer number", manifest, "v0.1.00", false},
		{"suffix on another version", manifest, "v0.2.0-rc1", false},
		{"spec version is not the manifest version", manifest, "v9.9.9", false},
		{"quoted manifest version", "metadata:\n  version: \"0.1.0\"\n", "v0.1.0", true},
		{"no metadata version", "metadata:\n  id: x\nspec:\n  version: 0.1.0\n", "v0.1.0", false},
		{"empty tag", manifest, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := runTagCheck(t, script, tt.manifest, tt.tag); got != tt.want {
				t.Fatalf("tag %q against %q: passed = %v, want %v", tt.tag, tt.name, got, tt.want)
			}
		})
	}
}

// TestReleaseTagCheckAcceptsTheShippedManifest ties the check to the real
// manifest, so bumping one without the tag convention cannot drift unnoticed.
func TestReleaseTagCheckAcceptsTheShippedManifest(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(release.yml) error = %v", err)
	}
	manifest, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatalf("os.ReadFile(manifest.yaml) error = %v", err)
	}
	var parsed struct {
		Metadata struct {
			Version string `yaml:"version"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(manifest, &parsed); err != nil || parsed.Metadata.Version == "" {
		t.Fatalf("manifest.yaml metadata.version = %q, error = %v", parsed.Metadata.Version, err)
	}
	script := tagCheckScript(t, raw)
	if !runTagCheck(t, script, string(manifest), "v"+parsed.Metadata.Version) {
		t.Fatalf("the tag check rejects v%s, the shipped manifest version", parsed.Metadata.Version)
	}
	if runTagCheck(t, script, string(manifest), "v"+parsed.Metadata.Version+".1") {
		t.Fatalf("the tag check accepts a tag that is not the shipped manifest version")
	}
}
