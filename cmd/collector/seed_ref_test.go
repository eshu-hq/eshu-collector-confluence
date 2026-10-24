// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	confluence "github.com/eshu-hq/eshu-collector-confluence"
)

var hex64 = strings.Repeat("a", 64)

func TestSeedImageRefPattern(t *testing.T) {
	t.Parallel()

	accept := []string{
		pinnedRef,
		"ghcr.io/eshu-hq/eshu-collector-confluence@sha256:" + hex64,
		"registry.example.invalid:5000/team/app@sha256:" + hex64,
		"localhost/app@sha256:" + hex64,
		"a.b-c_d/e.f@sha256:" + hex64,
	}
	refuse := map[string]string{
		"group reference":      "a$1b@sha256:" + hex64,
		"braced group":         "a${1}b@sha256:" + hex64,
		"double quote":         `ghcr.io/x"y@sha256:` + hex64,
		"single quote":         "ghcr.io/x'y@sha256:" + hex64,
		"backslash":            `ghcr.io/x\1@sha256:` + hex64,
		"ampersand":            "ghcr.io/x&y@sha256:" + hex64,
		"space":                "ghcr.io/x y@sha256:" + hex64,
		"leading space":        " ghcr.io/x@sha256:" + hex64,
		"trailing space":       "ghcr.io/x@sha256:" + hex64 + " ",
		"embedded newline":     "ghcr.io/x@sha256:" + hex64 + "\nimage: other",
		"two trailing newline": "ghcr.io/x@sha256:" + hex64 + "\n\n",
		"upper-case host":      "GHCR.io/x@sha256:" + hex64,
		"upper-case digest":    "ghcr.io/x@sha256:" + strings.ToUpper(hex64),
		"short digest":         "ghcr.io/x@sha256:" + hex64[:63],
		"long digest":          "ghcr.io/x@sha256:" + hex64 + "a",
		"tag instead of port":  "ghcr.io/x:v1@sha256:" + hex64,
		"tag and digest":       "ghcr.io/x:latest",
		"no digest":            "ghcr.io/x",
		"userinfo":             "user:pw@ghcr.io/x@sha256:" + hex64,
		"scheme":               "https://ghcr.io/x@sha256:" + hex64,
		"empty path segment":   "ghcr.io//x@sha256:" + hex64,
		"doubled separator":    "ghcr.io/a..b@sha256:" + hex64,
		"leading separator":    "-ghcr.io/x@sha256:" + hex64,
		"colon in yaml":        "ghcr.io/x: y@sha256:" + hex64,
		"comment":              "ghcr.io/x#y@sha256:" + hex64,
		"empty":                "",
	}
	for _, ref := range accept {
		if !digestPinnedRef.MatchString(ref) {
			t.Errorf("digestPinnedRef rejects %q, want accepted", ref)
		}
		if _, err := pinManifestImage(confluence.ManifestYAML, ref); err != nil {
			t.Errorf("pinManifestImage(%q) error = %v", ref, err)
		}
	}
	for name, ref := range refuse {
		if _, err := pinManifestImage(confluence.ManifestYAML, ref); err == nil {
			t.Errorf("%s: pinManifestImage(%q) error = nil, want a refusal", name, ref)
		}
	}
}

// TestSeedPinChangesOnlyTheArtifactImageByteForByte compares the whole pinned
// manifest with the original: the one byte range that differs is the image
// value, and the replacement is exactly the reference.
func TestSeedPinChangesOnlyTheArtifactImageByteForByte(t *testing.T) {
	t.Parallel()

	original := confluence.ManifestYAML
	row := regexp.MustCompile(`(?m)^[ \t]+image:[ \t]+(\S+)[ \t]*$`).FindSubmatch(original)
	if row == nil {
		t.Fatal("the embedded manifest has no image row")
	}
	want := bytes.Replace(original, row[1], []byte(pinnedRef), 1)
	got, err := pinManifestImage(original, pinnedRef)
	if err != nil {
		t.Fatalf("pinManifestImage() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("pinned manifest differs from the original beyond the image value:\n%s", got)
	}
	if bytes.Equal(got, original) {
		t.Fatal("pinning changed nothing")
	}
	again, err := pinManifestImage(got, pinnedRef)
	if err != nil || !bytes.Equal(again, got) {
		t.Errorf("pinning an already pinned manifest changed it (err = %v)", err)
	}
}

// TestStampImageRowsIsLiteral proves the replacement is never a regexp
// template. The reference pattern keeps template characters out of seed, so
// this checks the replacement itself with a value the pattern would refuse.
func TestStampImageRowsIsLiteral(t *testing.T) {
	t.Parallel()

	const value = `a$1b${1}&\1`
	manifest := []byte("spec:\n  artifacts:\n    - platform: linux/amd64\n      image: old\n")
	got, rows := stampImageRows(manifest, value)
	if rows != 1 {
		t.Fatalf("rows = %d, want 1", rows)
	}
	want := "spec:\n  artifacts:\n    - platform: linux/amd64\n      image: " + value + "\n"
	if string(got) != want {
		t.Fatalf("stamped manifest =\n%q\nwant\n%q", got, want)
	}
}

func TestSeedRefusesAManifestWithoutAnImageRow(t *testing.T) {
	t.Parallel()

	if _, err := pinManifestImage([]byte("spec:\n  artifacts: []\n"), pinnedRef); err == nil {
		t.Fatal("pinManifestImage() error = nil, want a refusal when no row can be pinned")
	}
}

func TestRunSeedToleratesExactlyOneTrailingNewline(t *testing.T) {
	t.Parallel()

	for name, tt := range map[string]struct {
		ref     string
		wantErr bool
	}{
		"clean":          {ref: pinnedRef},
		"one newline":    {ref: pinnedRef + "\n"},
		"two newlines":   {ref: pinnedRef + "\n\n", wantErr: true},
		"leading space":  {ref: " " + pinnedRef, wantErr: true},
		"trailing space": {ref: pinnedRef + " ", wantErr: true},
		"crlf":           {ref: pinnedRef + "\r\n", wantErr: true},
	} {
		for _, via := range []string{"flag", "env"} {
			dest := t.TempDir()
			args, env := []string{"--dest", dest}, map[string]string(nil)
			if via == "flag" {
				args = append(args, "--image-ref", tt.ref)
			} else {
				env = map[string]string{envImageRef: tt.ref}
			}
			err := runSeed(args, &bytes.Buffer{}, getenv(env))
			if (err != nil) != tt.wantErr {
				t.Errorf("%s via %s: runSeed() error = %v, wantErr %v", name, via, err, tt.wantErr)
			}
			if err == nil {
				manifest, _ := os.ReadFile(filepath.Join(dest, "manifest.yaml"))
				if !strings.Contains(string(manifest), "image: "+pinnedRef+"\n") {
					t.Errorf("%s via %s: manifest does not name the reference", name, via)
				}
			}
		}
	}
}

// TestSeedRefusalWritesNothing covers both a fresh destination and one that
// already holds a seeded home: a refused reference creates no directory, no
// temp file, and changes no existing byte or timestamp.
func TestSeedRefusalWritesNothing(t *testing.T) {
	t.Parallel()

	bad := []string{"a$1b@sha256:" + hex64, `ghcr.io/x"y@sha256:` + hex64}

	fresh := filepath.Join(t.TempDir(), "never", "created")
	for _, ref := range bad {
		if err := runSeed([]string{"--dest", fresh, "--image-ref", ref}, &bytes.Buffer{}, getenv(nil)); err == nil {
			t.Fatalf("runSeed(%q) error = nil, want a refusal", ref)
		}
		if _, err := os.Stat(filepath.Dir(fresh)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a refused reference created %s (stat error = %v)", filepath.Dir(fresh), err)
		}
	}

	dest := t.TempDir()
	if err := runSeed([]string{"--dest", dest, "--image-ref", pinnedRef}, &bytes.Buffer{}, getenv(nil)); err != nil {
		t.Fatalf("first seed error = %v", err)
	}
	before := snapshotDir(t, dest)
	for _, ref := range bad {
		if err := runSeed([]string{"--dest", dest, "--image-ref", ref}, &bytes.Buffer{}, getenv(nil)); err == nil {
			t.Fatalf("runSeed(%q) error = nil, want a refusal", ref)
		}
	}
	if after := snapshotDir(t, dest); after != before {
		t.Errorf("a refused reference touched the seeded home:\n before %s\n after  %s", before, after)
	}
}

// snapshotDir describes every file in dir by name, mode, size, mtime, and
// content hash, so a test can prove nothing changed.
func snapshotDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(mustRead(t, filepath.Join(dir, entry.Name())))
		lines = append(lines, fmt.Sprintf("%s %s %s %x", entry.Name(), info.Mode(), info.ModTime(), sum))
	}
	return strings.Join(lines, "\n")
}

func TestSeedWithAReferenceIsIdempotentAndLeavesNoTempFiles(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()
	args := []string{"--dest", dest, "--image-ref", pinnedRef}
	var first, second bytes.Buffer
	if err := runSeed(args, &first, getenv(nil)); err != nil {
		t.Fatalf("first seed error = %v", err)
	}
	before := snapshotDir(t, dest)
	if err := runSeed(args, &second, getenv(nil)); err != nil {
		t.Fatalf("second seed error = %v", err)
	}
	if !strings.Contains(first.String(), "(updated)") || !strings.Contains(second.String(), "(unchanged)") {
		t.Errorf("outputs = %q then %q, want updated then unchanged", first.String(), second.String())
	}
	if after := snapshotDir(t, dest); after != before {
		t.Errorf("an unchanged seed touched the files:\n before %s\n after  %s", before, after)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 2 {
		t.Errorf("seeded home holds %d entries, want collector and manifest.yaml only", len(entries))
	}
}

// TestBuiltSeedExitsNonZeroAndWritesNothingForABadReference runs the real
// executable: the exit status is what an init container sees.
func TestBuiltSeedExitsNonZeroAndWritesNothingForABadReference(t *testing.T) {
	t.Parallel()

	binary := builtBinary(t)
	for name, ref := range map[string]string{
		"group reference": "a$1b@sha256:" + hex64,
		"double quote":    `ghcr.io/x"y@sha256:` + hex64,
	} {
		dest := filepath.Join(t.TempDir(), "home")
		command := exec.CommandContext(t.Context(), binary, "seed", "--dest", dest, "--image-ref", ref)
		out, err := command.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() == 0 {
			t.Errorf("%s: seed error = %v, want a non-zero exit\n%s", name, err, out)
		}
		if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("%s: seed created %s (stat error = %v)", name, dest, statErr)
		}
		if strings.Contains(string(out), ref) {
			t.Errorf("%s: seed echoed the refused reference: %q", name, out)
		}
	}
}
