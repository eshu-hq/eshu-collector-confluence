// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"

	confluence "github.com/eshu-hq/eshu-collector-confluence"
)

const pinnedRef = "ghcr.io/eshu-hq/eshu-collector-confluence@sha256:1111111111111111111111111111111111111111111111111111111111111111"

func writeSource(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-binary")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSeedWritesTheBinaryAndTheEmbeddedManifest(t *testing.T) {
	t.Parallel()

	dest := filepath.Join(t.TempDir(), "nested", "component-home")
	source := writeSource(t, "binary-v1")
	status, err := seedInto(dest, source, confluence.ManifestYAML)
	if err != nil || status != "updated" {
		t.Fatalf("seedInto() = %q, %v, want updated", status, err)
	}
	binary, err := os.ReadFile(filepath.Join(dest, "collector"))
	if err != nil || string(binary) != "binary-v1" {
		t.Fatalf("collector = %q, %v", binary, err)
	}
	if info, _ := os.Stat(filepath.Join(dest, "collector")); info.Mode().Perm() != 0o755 {
		t.Errorf("collector mode = %v, want 0755", info.Mode().Perm())
	}
	manifest, err := os.ReadFile(filepath.Join(dest, "manifest.yaml"))
	if err != nil || !bytes.Equal(manifest, confluence.ManifestYAML) {
		t.Fatalf("manifest.yaml differs from the embedded manifest: %v", err)
	}
	if info, _ := os.Stat(filepath.Join(dest, "manifest.yaml")); info.Mode().Perm() != 0o644 {
		t.Errorf("manifest mode = %v, want 0644", info.Mode().Perm())
	}
}

func TestSeedIsIdempotentAndReplacesAChangedBinary(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()
	source := writeSource(t, "binary-v1")
	if _, err := seedInto(dest, source, confluence.ManifestYAML); err != nil {
		t.Fatalf("first seed error = %v", err)
	}
	before := statOf(t, filepath.Join(dest, "collector"), filepath.Join(dest, "manifest.yaml"))
	time.Sleep(20 * time.Millisecond)

	status, err := seedInto(dest, source, confluence.ManifestYAML)
	if err != nil || status != "unchanged" {
		t.Fatalf("second seed = %q, %v, want unchanged", status, err)
	}
	if after := statOf(t, filepath.Join(dest, "collector"), filepath.Join(dest, "manifest.yaml")); after != before {
		t.Errorf("unchanged inputs touched the files:\n before %s\n after  %s", before, after)
	}

	newSource := writeSource(t, "binary-v2")
	status, err = seedInto(dest, newSource, confluence.ManifestYAML)
	if err != nil || status != "updated" {
		t.Fatalf("changed seed = %q, %v, want updated", status, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "collector")); string(got) != "binary-v2" {
		t.Errorf("collector = %q, want the new binary", got)
	}
}

func statOf(t *testing.T, paths ...string) string {
	t.Helper()
	var parts []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, info.ModTime().String()+info.Mode().String())
	}
	return strings.Join(parts, "|")
}

func TestSeedRepairsAWrongModeOrCorruptedFile(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()
	source := writeSource(t, "binary-v1")
	if _, err := seedInto(dest, source, confluence.ManifestYAML); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dest, "collector"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "manifest.yaml"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, err := seedInto(dest, source, confluence.ManifestYAML); err != nil || status != "updated" {
		t.Fatalf("seedInto() = %q, %v, want updated", status, err)
	}
	if info, _ := os.Stat(filepath.Join(dest, "collector")); info.Mode().Perm() != 0o755 {
		t.Errorf("collector mode = %v, want it restored to 0755", info.Mode().Perm())
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "manifest.yaml")); !bytes.Equal(got, confluence.ManifestYAML) {
		t.Error("a tampered manifest was not restored")
	}
}

func TestSeedFailureLeavesNoPartialFiles(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()
	if _, err := seedInto(dest, filepath.Join(dest, "missing"), confluence.ManifestYAML); err == nil {
		t.Fatal("seedInto() error = nil, want a failure for a missing source")
	}
	entries, _ := os.ReadDir(dest)
	for _, entry := range entries {
		t.Errorf("failed seed left %q behind", entry.Name())
	}
	// A destination that is a file cannot be created.
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o644)
	if _, err := seedInto(filepath.Join(file, "sub"), writeSource(t, "x"), nil); err == nil {
		t.Error("seedInto() under a regular file error = nil, want a failure")
	}
}

func TestSeedPinsTheManifestToADigestPinnedImageRef(t *testing.T) {
	t.Parallel()

	pinned, err := pinManifestImage(confluence.ManifestYAML, pinnedRef)
	if err != nil {
		t.Fatalf("pinManifestImage() error = %v", err)
	}
	if !strings.Contains(string(pinned), "image: "+pinnedRef+"\n") || strings.Contains(string(pinned), "sha256:0000") {
		t.Fatalf("manifest not pinned:\n%s", pinned)
	}
	if len(strings.Split(string(pinned), "\n")) != len(strings.Split(string(confluence.ManifestYAML), "\n")) {
		t.Error("pinning changed the line count of the manifest")
	}
	for _, bad := range []string{
		"ghcr.io/eshu-hq/eshu-collector-confluence:latest", "ghcr.io/eshu-hq/eshu-collector-confluence@sha256:abc",
		"ghcr.io/eshu-hq/eshu-collector-confluence@sha256:" + strings.Repeat("G", 64), "", "has space@sha256:" + strings.Repeat("a", 64),
		pinnedRef + "\nmalicious: true",
	} {
		if _, err := pinManifestImage(confluence.ManifestYAML, bad); err == nil {
			t.Errorf("pinManifestImage(%q) error = nil, want a refusal", bad)
		}
	}
}

func TestRunSeedReadsTheFlagAndTheEnvironmentVariable(t *testing.T) {
	t.Parallel()

	envPinned := strings.Replace(pinnedRef, "1111", "2222", 1)
	tests := map[string]struct {
		args    []string
		env     map[string]string
		wantRef string
		wantErr string
	}{
		"no pin":         {args: nil, wantRef: "sha256:0000"},
		"flag":           {args: []string{"--image-ref", pinnedRef}, wantRef: pinnedRef},
		"env":            {env: map[string]string{"COLLECTOR_IMAGE_REF": envPinned}, wantRef: envPinned},
		"flag beats env": {args: []string{"--image-ref", pinnedRef}, env: map[string]string{"COLLECTOR_IMAGE_REF": envPinned}, wantRef: pinnedRef},
		"bad ref":        {args: []string{"--image-ref", "ghcr.io/x:latest"}, wantErr: "digest-pinned"},
		"bad env ref":    {env: map[string]string{"COLLECTOR_IMAGE_REF": "x"}, wantErr: "digest-pinned"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dest := t.TempDir()
			var stdout bytes.Buffer
			err := runSeed(append([]string{"--dest", dest}, tt.args...), &stdout, getenv(tt.env))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("runSeed() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("runSeed() error = %v", err)
			}
			manifest, _ := os.ReadFile(filepath.Join(dest, "manifest.yaml"))
			if !strings.Contains(string(manifest), tt.wantRef) {
				t.Errorf("manifest image row lacks %q", tt.wantRef)
			}
			if !strings.Contains(stdout.String(), "seeded collector and manifest.yaml") {
				t.Errorf("stdout = %q", stdout.String())
			}
		})
	}
}

func TestRunSeedRejectsBadInvocations(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"no dest":      nil,
		"blank dest":   {"--dest", "  "},
		"unknown flag": {"--dest", "x", "--nope"},
		"positional":   {"--dest", t.TempDir(), "extra"},
		"help":         {"-h"},
	} {
		var stdout bytes.Buffer
		err := runSeed(args, &stdout, getenv(nil))
		if err == nil {
			t.Errorf("%s: runSeed() error = nil, want a failure", name)
		}
	}
}

// TestSeededBinaryServesAClaim builds the real binary, seeds it, and runs the
// seeded copy against a request: the seeded file is a working collector.
func TestSeededBinaryServesAClaim(t *testing.T) {
	built := builtBinary(t)
	work := t.TempDir()
	dest := filepath.Join(work, "component-home")
	seed := exec.CommandContext(t.Context(), built, "seed", "--dest", dest, "--image-ref", pinnedRef)
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed error = %v\n%s", err, out)
	}
	seeded := filepath.Join(dest, "collector")
	if info, err := os.Stat(seeded); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("seeded collector: %v %v", info, err)
	}
	if a, _ := os.ReadFile(built); !bytes.Equal(a, mustRead(t, seeded)) {
		t.Fatal("the seeded binary differs from the binary that seeded it")
	}

	server := confluenceStub(t)
	run := exec.CommandContext(t.Context(), seeded, "--sdk-stdio")
	run.Stdin = strings.NewReader(requestJSON(t, server.URL))
	run.Env = append(os.Environ(), "ESHU_CONFLUENCE_BEARER_TOKEN="+secret)
	var stdout, stderr bytes.Buffer
	run.Stdout, run.Stderr = &stdout, &stderr
	if err := run.Run(); err != nil {
		t.Fatalf("seeded collector error = %v\n%s", err, stderr.String())
	}
	var result sdk.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not a result: %v", err)
	}
	if result.State != sdk.ResultComplete || len(result.Facts) == 0 {
		t.Fatalf("result = %q with %d facts, want a complete snapshot", result.State, len(result.Facts))
	}
	if bytes.Contains(stdout.Bytes(), []byte("\n  ")) {
		t.Error("stdout is indented; the host caps stdout, so it must be compact")
	}
	if strings.Contains(stdout.String()+stderr.String(), secret) {
		t.Error("the credential reached an output stream")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
