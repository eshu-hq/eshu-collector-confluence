// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	buildDir  string
	buildPath string
	buildErr  error
)

// TestMain removes the shared test binary after the package's tests finish.
func TestMain(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

// builtBinary compiles cmd/collector once for every test that runs the real
// executable and returns its path. Tests that need it are skipped in -short.
func builtBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool is not on PATH")
	}
	buildOnce.Do(func() {
		buildDir, buildErr = os.MkdirTemp("", "collector-build-")
		if buildErr != nil {
			return
		}
		buildPath = filepath.Join(buildDir, "collector")
		command := exec.Command(goTool, "build", "-trimpath", "-o", buildPath, ".")
		command.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := command.CombinedOutput(); err != nil {
			buildErr = &buildFailure{err: err, output: string(out)}
		}
	})
	if buildErr != nil {
		t.Fatalf("go build: %v", buildErr)
	}
	return buildPath
}

type buildFailure struct {
	err    error
	output string
}

func (b *buildFailure) Error() string { return b.err.Error() + "\n" + b.output }
