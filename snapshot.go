// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// snapshot is the bounded page set read for one claimed scope.
type snapshot struct {
	space Space
	// pages holds the latest current version of each readable page, sorted by
	// page id so output order never depends on provider or worker timing.
	pages []Page
	// skipped counts pages left out because the credential cannot read them.
	skipped int
	// truncated reports that a cursor walk stopped before the provider ran out.
	truncated bool
}

// readSnapshot lists and fetches the bounded page set for the configured
// target: one space or one root page tree.
func readSnapshot(ctx context.Context, client Client, cfg Config) (snapshot, error) {
	if cfg.SpaceID != "" {
		return readSpace(ctx, client, cfg)
	}
	return readPageTree(ctx, client, cfg)
}

func readSpace(ctx context.Context, client Client, cfg Config) (snapshot, error) {
	space, err := client.GetSpace(ctx, cfg.SpaceID)
	if err != nil {
		return snapshot{}, fmt.Errorf("get confluence space: %w", err)
	}
	space = Space{ID: cleanText(space.ID), Key: cleanText(space.Key), Name: cleanText(space.Name)}
	listed, truncated, err := client.ListSpacePages(ctx, cfg.SpaceID, cfg.PageLimit, cfg.MaxTotalPages)
	if err != nil {
		return snapshot{}, fmt.Errorf("list confluence space pages: %w", err)
	}
	listedPages := make([]Page, 0, len(listed))
	ids := make([]string, 0, len(listed))
	for _, page := range listed {
		page = cleanPage(page)
		if strings.TrimSpace(page.ID) == "" {
			continue
		}
		listedPages = append(listedPages, page)
		ids = append(ids, page.ID)
	}
	fetched, skipped, err := fetchPages(ctx, client, ids, cfg.FetchConcurrency)
	if err != nil {
		return snapshot{}, err
	}
	pages := make([]Page, 0, len(fetched))
	for _, item := range fetched {
		pages = append(pages, mergePageDetails(listedPages[item.index], item.page))
	}
	return snapshot{
		space:     space,
		pages:     latestCurrentPages(pages),
		skipped:   skipped,
		truncated: truncated,
	}, nil
}

func readPageTree(ctx context.Context, client Client, cfg Config) (snapshot, error) {
	ids, truncated, err := client.ListPageTree(ctx, cfg.RootPageID, cfg.PageLimit, cfg.MaxTotalPages)
	if err != nil {
		return snapshot{}, fmt.Errorf("list confluence page tree: %w", err)
	}
	fetched, skipped, err := fetchPages(ctx, client, ids, cfg.FetchConcurrency)
	if err != nil {
		return snapshot{}, err
	}
	pages := make([]Page, 0, len(fetched))
	for _, item := range fetched {
		pages = append(pages, item.page)
	}
	return snapshot{
		space:     Space{ID: firstSpaceID(pages), Key: cfg.SpaceKey},
		pages:     latestCurrentPages(pages),
		skipped:   skipped,
		truncated: truncated,
	}, nil
}

// fetchedPage is one successfully fetched page and the position of its id in
// the input list.
type fetchedPage struct {
	index int
	page  Page
}

// fetchPages fetches page details with at most workers requests in flight. A
// page the credential cannot read is skipped and counted. Results keep input
// order. The first failure stops new fetches from being dispatched but lets the
// requests already in flight finish, and the failure at the lowest input index
// wins. Pages are dispatched in input order, so every index below a failing one
// was already dispatched and the winner does not depend on worker timing.
func fetchPages(ctx context.Context, client Client, ids []string, workers int) ([]fetchedPage, int, error) {
	if workers < 1 {
		workers = 1
	}
	type outcome struct {
		page    Page
		denied  bool
		err     error
		started bool
	}
	outcomes := make([]outcome, len(ids))
	failed := make(chan struct{})
	var failOnce sync.Once

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				page, err := client.GetPage(ctx, ids[index])
				outcomes[index].started = true
				switch {
				case err == nil:
					outcomes[index].page = cleanPage(page)
				case errors.Is(err, ErrPermissionDenied):
					outcomes[index].denied = true
				default:
					outcomes[index].err = fmt.Errorf("get confluence page: %w", err)
					failOnce.Do(func() { close(failed) })
				}
			}
		}()
	}
dispatch:
	for index := range ids {
		select {
		case jobs <- index:
		case <-failed:
			break dispatch
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()

	// The caller's context failing outranks any fetch error it caused.
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	for index := range outcomes {
		if outcomes[index].err != nil {
			return nil, 0, outcomes[index].err
		}
	}
	fetched := make([]fetchedPage, 0, len(ids))
	skipped := 0
	for index, item := range outcomes {
		switch {
		case item.denied:
			skipped++
		case item.started:
			fetched = append(fetched, fetchedPage{index: index, page: item.page})
		}
	}
	return fetched, skipped, nil
}

// mergePageDetails fills fields the detail response left empty from the list
// response, so a sparse detail never erases data the list already returned.
func mergePageDetails(listed Page, detail Page) Page {
	if detail.ID == "" {
		detail.ID = listed.ID
	}
	if detail.Status == "" {
		detail.Status = listed.Status
	}
	if detail.Title == "" {
		detail.Title = listed.Title
	}
	if detail.SpaceID == "" {
		detail.SpaceID = listed.SpaceID
	}
	if detail.ParentID == "" {
		detail.ParentID = listed.ParentID
	}
	if detail.OwnerID == "" {
		detail.OwnerID = listed.OwnerID
	}
	if detail.AuthorID == "" {
		detail.AuthorID = listed.AuthorID
	}
	if detail.Version.Number == 0 {
		detail.Version = listed.Version
	}
	if detail.Body.Storage.Value == "" {
		detail.Body = listed.Body
	}
	if len(detail.Labels.Results) == 0 {
		detail.Labels = listed.Labels
	}
	if detail.Links.Base == "" {
		detail.Links.Base = listed.Links.Base
	}
	if detail.Links.WebUI == "" {
		detail.Links.WebUI = listed.Links.WebUI
	}
	return detail
}

// latestCurrentPages keeps the highest-version current page for each id and
// returns them sorted by id. Pages without an id cannot be identified and are
// dropped.
func latestCurrentPages(pages []Page) []Page {
	latest := map[string]Page{}
	for _, page := range pages {
		if strings.TrimSpace(page.ID) == "" {
			continue
		}
		if page.Status != "" && page.Status != "current" {
			continue
		}
		if existing, ok := latest[page.ID]; !ok || page.Version.Number > existing.Version.Number {
			latest[page.ID] = page
		}
	}
	out := make([]Page, 0, len(latest))
	for _, page := range latest {
		out = append(out, page)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func firstSpaceID(pages []Page) string {
	for _, page := range pages {
		if strings.TrimSpace(page.SpaceID) != "" {
			return page.SpaceID
		}
	}
	return ""
}

// cleanPage strips characters the store cannot hold from every provider string
// the facts will carry.
func cleanPage(page Page) Page {
	page.ID = cleanText(page.ID)
	page.Status = cleanText(page.Status)
	page.Title = cleanText(page.Title)
	page.SpaceID = cleanText(page.SpaceID)
	page.ParentID = cleanText(page.ParentID)
	page.OwnerID = cleanText(page.OwnerID)
	page.AuthorID = cleanText(page.AuthorID)
	page.Version.CreatedAt = cleanText(page.Version.CreatedAt)
	page.Version.AuthorID = cleanText(page.Version.AuthorID)
	page.Body.Storage.Value = cleanText(page.Body.Storage.Value)
	page.Body.Storage.Representation = cleanText(page.Body.Storage.Representation)
	page.Links.Base = cleanText(page.Links.Base)
	page.Links.WebUI = cleanText(page.Links.WebUI)
	labels := make([]Label, 0, len(page.Labels.Results))
	for _, label := range page.Labels.Results {
		labels = append(labels, Label{Name: cleanText(label.Name)})
	}
	page.Labels = labelResults{Results: labels}
	return page
}
