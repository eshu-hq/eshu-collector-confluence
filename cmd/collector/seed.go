// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	confluence "github.com/eshu-hq/eshu-collector-confluence"
)

const (
	seededBinaryName   = "collector"
	seededManifestName = "manifest.yaml"
	envImageRef        = "COLLECTOR_IMAGE_REF"
)

var (
	// digestPinnedRef is the only image reference seed accepts: a lower-case
	// registry host (optionally with a port) and repository path, then a full
	// sha256 digest. It admits no quote, backslash, dollar sign, whitespace,
	// newline, or tag, so the value is safe to write into the manifest as a
	// plain YAML scalar.
	digestPinnedRef = regexp.MustCompile(
		`^[a-z0-9]+([._-][a-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-][a-z0-9]+)*)*@sha256:[0-9a-f]{64}$`)
	manifestImageRow = regexp.MustCompile(`(?m)^([ \t]+image:[ \t]+)\S+[ \t]*$`)
)

// runSeed implements `collector seed --dest DIR [--image-ref REF]`.
//
// It writes DIR/collector (this static binary, mode 0755) and DIR/manifest.yaml
// (the embedded manifest) so an init container built from a shell-less image
// can populate a component home; the Eshu CLI then installs, enables, and
// grants the component. A file whose bytes and mode already match is left
// untouched, so re-running with unchanged inputs changes nothing, and a changed
// binary is replaced atomically (temp file in DIR, then rename) before the
// worker starts.
//
// The manifest ships with a placeholder artifact digest because an image cannot
// name its own digest. When the deployer passes the digest-pinned image
// reference the pod is actually running, via --image-ref or COLLECTOR_IMAGE_REF,
// the seeded manifest names that image.
func runSeed(args []string, stdout io.Writer, getenv func(string) string) error {
	flags := flag.NewFlagSet("collector seed", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dest := flags.String("dest", "", "directory to write the collector binary and manifest.yaml into")
	imageRef := flags.String("image-ref", getenv(envImageRef), "digest-pinned image reference to record in the manifest artifact")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	if flags.NArg() > 0 {
		return errors.New("seed: unexpected arguments")
	}
	if strings.TrimSpace(*dest) == "" {
		return errors.New("seed: --dest is required")
	}
	manifest := confluence.ManifestYAML
	// One trailing newline is tolerated, so a reference read from a file or
	// command output works. Anything else, including other whitespace, is
	// validated as part of the reference and refused if it does not belong.
	if ref := strings.TrimSuffix(*imageRef, "\n"); ref != "" {
		var err error
		if manifest, err = pinManifestImage(manifest, ref); err != nil {
			return err
		}
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("seed: locate binary: %w", err)
	}
	changed, err := seedInto(*dest, self, manifest)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "seeded %s and %s (%s)\n", seededBinaryName, seededManifestName, changed)
	return nil
}

// pinManifestImage rewrites the image of every manifest artifact to ref, which
// must be a digest-pinned reference. The replacement is literal: ref is never
// interpreted as a regexp template, so no character in it can expand a group.
// Only the value after "image:" changes; every other byte of the manifest is
// preserved. A manifest with no artifact image row is an error, not a silent
// no-op.
func pinManifestImage(manifest []byte, ref string) ([]byte, error) {
	if !digestPinnedRef.MatchString(ref) {
		return nil, errors.New("seed: --image-ref must be a digest-pinned reference (name[:port]/path@sha256:<64 hex>)")
	}
	pinned, rows := stampImageRows(manifest, ref)
	if rows == 0 {
		return nil, errors.New("seed: the manifest has no artifact image row to pin")
	}
	return pinned, nil
}

// stampImageRows writes value after every "image:" row key and returns the new
// manifest and the number of rows it changed. The replacement is a literal byte
// copy, never a regexp template, so $1, ${1}, & and backslashes in value stay
// as written.
func stampImageRows(manifest []byte, value string) ([]byte, int) {
	rows := 0
	stamped := manifestImageRow.ReplaceAllFunc(manifest, func(row []byte) []byte {
		rows++
		prefix := manifestImageRow.FindSubmatch(row)[1]
		return append(append(make([]byte, 0, len(prefix)+len(value)), prefix...), value...)
	})
	return stamped, rows
}

// seedInto writes the binary at source and the manifest into dest and reports
// "unchanged" when both files already held the same bytes and mode.
func seedInto(dest, source string, manifest []byte) (string, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("seed: create destination: %w", err)
	}
	binaryChanged, err := installFile(filepath.Join(dest, seededBinaryName), 0o755, func() (io.ReadCloser, error) { return os.Open(source) })
	if err != nil {
		return "", err
	}
	manifestChanged, err := installFile(filepath.Join(dest, seededManifestName), 0o644, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(manifest)), nil
	})
	if err != nil {
		return "", err
	}
	if binaryChanged || manifestChanged {
		return "updated", nil
	}
	return "unchanged", nil
}

// installFile makes target hold the content open returns, with mode. It does
// nothing when the target already matches.
func installFile(target string, mode os.FileMode, open func() (io.ReadCloser, error)) (bool, error) {
	name := filepath.Base(target)
	if same, err := matches(target, mode, open); err != nil {
		return false, err
	} else if same {
		return false, nil
	}
	in, err := open()
	if err != nil {
		return false, fmt.Errorf("seed: open source for %s: %w", name, err)
	}
	defer func() { _ = in.Close() }()
	temp, err := os.CreateTemp(filepath.Dir(target), ".seed-*")
	if err != nil {
		return false, fmt.Errorf("seed: create temp file: %w", err)
	}
	tempName := temp.Name()
	cleanup := func() { _ = os.Remove(tempName) }
	if _, err := io.Copy(temp, in); err != nil {
		_ = temp.Close()
		cleanup()
		return false, fmt.Errorf("seed: write %s: %w", name, err)
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return false, fmt.Errorf("seed: close %s: %w", name, err)
	}
	if err := os.Chmod(tempName, mode); err != nil {
		cleanup()
		return false, fmt.Errorf("seed: chmod %s: %w", name, err)
	}
	if err := os.Rename(tempName, target); err != nil {
		cleanup()
		return false, fmt.Errorf("seed: install %s: %w", name, err)
	}
	return true, nil
}

// matches reports whether target already holds the same bytes with the same
// permission bits as the content open returns.
func matches(target string, mode os.FileMode, open func() (io.ReadCloser, error)) (bool, error) {
	info, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("seed: stat %s: %w", filepath.Base(target), err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		return false, nil
	}
	want, err := digestOf(open)
	if err != nil {
		return false, fmt.Errorf("seed: read source for %s: %w", filepath.Base(target), err)
	}
	have, err := digestOf(func() (io.ReadCloser, error) { return os.Open(target) })
	if err != nil {
		return false, nil //nolint:nilerr // an unreadable target is replaced, not an error
	}
	return want == have, nil
}

func digestOf(open func() (io.ReadCloser, error)) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	in, err := open()
	if err != nil {
		return sum, err
	}
	defer func() { _ = in.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, in); err != nil {
		return sum, err
	}
	copy(sum[:], hash.Sum(nil))
	return sum, nil
}
