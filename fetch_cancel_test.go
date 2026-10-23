// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

// cancelAfterFirstClient serves a fixed page list and cancels the caller's
// context as the first detail read returns successfully. Every later read also
// succeeds, so nothing but the context records that the walk was cut short.
type cancelAfterFirstClient struct {
	cancel context.CancelFunc
	ids    []string
	reads  atomic.Int32
}

func (c *cancelAfterFirstClient) GetSpace(context.Context, string) (Space, error) {
	return Space{ID: "12345", Key: "ENG", Name: "Engineering"}, nil
}

func (c *cancelAfterFirstClient) ListSpacePages(context.Context, string, int, int) ([]Page, bool, error) {
	pages := make([]Page, 0, len(c.ids))
	for _, id := range c.ids {
		pages = append(pages, Page{ID: id, Status: "current", Title: "page " + id, SpaceID: "12345"})
	}
	return pages, false, nil
}

func (c *cancelAfterFirstClient) ListPageTree(context.Context, string, int, int) ([]string, bool, error) {
	return c.ids, false, nil
}

func (c *cancelAfterFirstClient) GetPage(_ context.Context, id string) (Page, error) {
	if c.reads.Add(1) == 1 {
		c.cancel()
	}
	return Page{ID: id, Status: "current", Title: "page " + id, SpaceID: "12345"}, nil
}

func manyPageIDs(n int) []string {
	ids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		ids = append(ids, fmt.Sprintf("%d", 1000+i))
	}
	return ids
}

// TestFetchPagesReportsACancelledWalkInsteadOfASmallerSnapshot pins review
// mutant T3. When the context ends between dispatches the loop stops handing
// out pages without recording an error, so only the ctx.Err() check keeps a
// half-read page set from returning as if it were complete. Many pages make
// the dispatch race lose with certainty, not by luck.
func TestFetchPagesReportsACancelledWalkInsteadOfASmallerSnapshot(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := &cancelAfterFirstClient{cancel: cancel, ids: manyPageIDs(200)}

	fetched, skipped, err := fetchPages(ctx, client, client.ids, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("fetchPages() error = %v with %d of %d pages, want context.Canceled", err, len(fetched), len(client.ids))
	}
	if len(fetched) != 0 || skipped != 0 {
		t.Fatalf("fetchPages() returned %d pages and %d skipped alongside the error, want none", len(fetched), skipped)
	}
}

// TestCollectRetriesWhenTheContextEndsMidWalk checks the whole claim path with
// one detail worker: the context ends after the first page read, and the
// result is the retryable deadline class, never a complete or partial snapshot.
func TestCollectRetriesWhenTheContextEndsMidWalk(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := &cancelAfterFirstClient{cancel: cancel, ids: manyPageIDs(200)}
	cfg := Config{SpaceID: "12345", ClaimTimeout: testClaim().Deadline.Sub(testNow()), PageLimit: 100, MaxTotalPages: 5000, FetchConcurrency: 1}
	collector := &Collector{Config: cfg, Client: client, Clock: fixedClock()}

	outcome := collector.Collect(ctx, testClaim())
	if outcome.State != sdk.ResultRetryable || failureClassOf(t, outcome) != FailureDeadline {
		t.Fatalf("state %q class %q, want retryable %s", outcome.State, failureClassOf(t, outcome), FailureDeadline)
	}
	if len(outcome.Facts) != 0 {
		t.Fatalf("a cancelled walk returned %d facts, want none", len(outcome.Facts))
	}
	assertHostAccepts(t, outcome)
}
