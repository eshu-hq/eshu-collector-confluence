// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

// Package confluence collects read-only Confluence page evidence for one
// claimed scope and returns it as collector-sdk/v1alpha1 documentation facts.
//
// One claim is one snapshot generation of one Confluence space or one root
// page tree. The host (the Eshu component-extension host, process adapter)
// sends one bounded Request on stdin and reads one Result from stdout. The
// package holds no Postgres, graph, queue, lease, or workflow handle and
// imports only the published SDK modules.
//
// The result carries the four source-neutral documentation kinds
// (documentation_source, documentation_document, documentation_section,
// documentation_link). The host builds the core envelope, so FactID values are
// the host's; kind, stable key, payload, scope, and generation are this
// package's contract.
//
// Boundary: this package imports only the published SDK modules, the standard
// library, golang.org/x/net/html, and gopkg.in/yaml.v3. boundary_test.go
// enforces it.
package confluence
