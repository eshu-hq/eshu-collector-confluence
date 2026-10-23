// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const realDigest = "ghcr.io/eshu-hq/eshu-collector-confluence@sha256:2222222222222222222222222222222222222222222222222222222222222222"

// runPinDigest runs scripts/pin-digest.sh with a stub `docker` first on PATH.
func runPinDigest(t *testing.T, arg, dockerOutput string) (string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	bin := t.TempDir()
	stub := "#!/bin/sh\nprintf '%s\\n' '" + dockerOutput + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), bash, filepath.Join("scripts", "pin-digest.sh"), arg)
	command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := command.CombinedOutput()
	return string(out), err
}

func TestPinDigestPrintsTheSeedReference(t *testing.T) {
	t.Parallel()

	for name, tt := range map[string]struct{ arg, docker string }{
		"digest ref passes through": {realDigest, "unused"},
		"tag resolves via docker":   {"ghcr.io/eshu-hq/eshu-collector-confluence:v0.1.0", realDigest},
	} {
		out, err := runPinDigest(t, tt.arg, tt.docker)
		if err != nil {
			t.Fatalf("%s: error = %v\n%s", name, err, out)
		}
		if !strings.Contains(out, "--image-ref "+realDigest) || !strings.Contains(out, "COLLECTOR_IMAGE_REF="+realDigest) {
			t.Errorf("%s: output lacks the seed reference:\n%s", name, out)
		}
	}
}

func TestPinDigestRefusesUnpinnedOrPlaceholderReferences(t *testing.T) {
	t.Parallel()

	zero := "ghcr.io/eshu-hq/eshu-collector-confluence@sha256:" + strings.Repeat("0", 64)
	for name, tt := range map[string]struct{ arg, docker string }{
		"docker returns a bare tag": {"ghcr.io/eshu-hq/eshu-collector-confluence:v0.1.0", "ghcr.io/eshu-hq/eshu-collector-confluence:v0.1.0"},
		"short digest":              {"ghcr.io/eshu-hq/eshu-collector-confluence@sha256:abc", "unused"},
		"placeholder digest":        {zero, "unused"},
		"injected text":             {realDigest + " --evil", "unused"},
	} {
		if out, err := runPinDigest(t, tt.arg, tt.docker); err == nil {
			t.Errorf("%s: pin-digest.sh succeeded, want a refusal:\n%s", name, out)
		}
	}
}
