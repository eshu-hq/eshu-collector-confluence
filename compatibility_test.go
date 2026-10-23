// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"strings"
	"testing"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

// supportedCoreRange is the manifest compatibleCore this collector pins,
// matching the sdk-compatibility row for SDK v0.2.0. The collector emits only
// fields the published SDK v0.2.0 defines, so any core in the range decodes
// its results.
const supportedCoreRange = ">=0.0.5 <0.2.0"

// TestCompatibilityMatrixSupportedPair proves the pinned SDK v0.2.0 protocol
// and manifest range hold together and a result passes the host checks.
func TestCompatibilityMatrixSupportedPair(t *testing.T) {
	t.Parallel()

	manifest := loadManifest(t)
	if manifest.Spec.CompatibleCore != supportedCoreRange {
		t.Fatalf("CompatibleCore = %q, want %q", manifest.Spec.CompatibleCore, supportedCoreRange)
	}
	if manifest.Spec.Runtime.SDKProtocol != sdk.ProtocolVersionV1Alpha1 {
		t.Fatalf("SDKProtocol = %q, want %q", manifest.Spec.Runtime.SDKProtocol, sdk.ProtocolVersionV1Alpha1)
	}
	if hostRejectsCoreRange(manifest.Spec.CompatibleCore, "0.1.0") {
		t.Fatalf("pinned range %q must cover supported core 0.1.0", manifest.Spec.CompatibleCore)
	}
	if err := hostChecks(t, manifest, loadConformanceFixture(t, "complete")); err != nil {
		t.Fatalf("supported pair rejected: %v", err)
	}
}

// TestCompatibilityMatrixRejectsUnsupportedVersions proves the checks fail
// closed on an unknown wire protocol, an unversioned fact kind, and a manifest
// whose core range no longer covers the running core.
func TestCompatibilityMatrixRejectsUnsupportedVersions(t *testing.T) {
	t.Parallel()

	complete := loadConformanceFixture(t, "complete")

	unsupported := complete
	unsupported.ProtocolVersion = "collector-sdk/v9alpha9"
	if err := hostChecks(t, loadManifest(t), unsupported); err == nil {
		t.Fatal("unsupported protocol: error = nil, want a rejection")
	}

	unversioned := loadManifest(t)
	unversioned.Spec.EmittedFacts[0].SchemaVersions = nil
	if err := hostChecks(t, unversioned, complete); err == nil {
		t.Fatal("unversioned fact kind: error = nil, want a rejection")
	}

	if !hostRejectsCoreRange(">=9.0.0 <10.0.0", "0.1.0") {
		t.Fatal("future core range must not cover running core 0.1.0")
	}
}

// hostRejectsCoreRange mirrors the host's spec.compatibleCore enforcement for
// the space-separated comparators this manifest pins (>= lower bound, < upper
// bound) with numeric segment comparison. Full range parsing stays with the
// core host; this locks the manifest's pin, not the host's parser.
func hostRejectsCoreRange(manifestRange, runningCore string) bool {
	return !coreRangeCovers(manifestRange, runningCore)
}

func coreRangeCovers(manifestRange, runningCore string) bool {
	running := parseCoreVersion(runningCore)
	if running == nil {
		return false
	}
	for _, comparator := range strings.Fields(manifestRange) {
		op, rest := "", comparator
		for _, candidate := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(rest, candidate) {
				op, rest = candidate, strings.TrimPrefix(rest, candidate)
				break
			}
		}
		bound := parseCoreVersion(rest)
		if bound == nil {
			return false
		}
		cmp := compareCoreVersions(running, bound)
		switch op {
		case ">=":
			if cmp < 0 {
				return false
			}
		case "<":
			if cmp >= 0 {
				return false
			}
		case ">":
			if cmp <= 0 {
				return false
			}
		case "<=":
			if cmp > 0 {
				return false
			}
		case "=":
			if cmp != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func parseCoreVersion(version string) []int {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil
	}
	if i := strings.IndexAny(version, "-+"); i >= 0 {
		version = version[:i]
	}
	segments := strings.Split(version, ".")
	if len(segments) < 1 || len(segments) > 3 {
		return nil
	}
	numbers := make([]int, 0, len(segments))
	for _, segment := range segments {
		number := 0
		for _, digit := range []byte(segment) {
			if digit < '0' || digit > '9' {
				return nil
			}
			number = number*10 + int(digit-'0')
		}
		numbers = append(numbers, number)
	}
	return numbers
}

func compareCoreVersions(left, right []int) int {
	width := max(len(left), len(right))
	for i := 0; i < width; i++ {
		var l, r int
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		if l != r {
			if l < r {
				return -1
			}
			return 1
		}
	}
	return 0
}
