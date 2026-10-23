// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import _ "embed"

// ManifestYAML is the component manifest this binary ships with. `collector
// seed` writes it next to the binary so the deployed manifest and binary can
// never disagree, and tests parse it to pin the contract this package emits.
//
//go:embed manifest.yaml
var ManifestYAML []byte
