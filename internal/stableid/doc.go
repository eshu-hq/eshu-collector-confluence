// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

// Package stableid derives the deterministic fact stable keys the Eshu core
// documentation path uses, so a fact emitted by this collector carries the
// same stable key the in-tree Confluence collector emitted for the same page.
//
// Replace this package with the published eshu-sdk helper when
// eshu-hq/eshu#8851 lands. Until then it is a verbatim copy of the pieces this
// collector uses: facts.StableID (go/internal/facts/encode/stable.go) and the
// documentation Source, Document, Section, and Link stable-ID helpers
// (go/internal/facts/docs/documentation.go). stableid_test.go pins the output
// to golden values computed from the in-tree functions.
package stableid
