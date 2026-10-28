// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestImageLaneRunsOnGoSources keeps the shipped artifact validated on
// PRs that change what it embeds: the Dockerfile COPYs go.mod/go.sum,
// COPYs the whole context (which keeps .go sources), and go-builds the
// binary, so a Go-source or go.mod PR must select the image lane. A
// go.mod toolchain bump past the pinned base would otherwise pass every
// Go lane yet break docker build, surfacing only as post-merge red main.
//
// This repo routes lanes through the changes-job decide script
// (run-image output), not through dorny filter outputs in the image
// job's if, so the test pins the decide wiring instead: the go filter
// covers **/*.go and the deps filter covers go.mod/go.sum, and the
// run_image assignment must answer to both.
func TestImageLaneRunsOnGoSources(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow workflowDoc
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	changes, ok := workflow.Jobs["changes"]
	if !ok {
		t.Fatal("ci.yml has no changes job")
	}
	var decide string
	for _, step := range changes.Steps {
		if step.Name == "Decide which lanes run" {
			decide = step.Run
		}
	}
	if decide == "" {
		t.Fatal("changes job has no Decide which lanes run step")
	}
	idx := strings.Index(decide, "run_image=true")
	if idx < 0 {
		t.Fatal("decide script never sets run_image=true")
	}
	cond := decide[:idx]
	if start := strings.LastIndex(cond, "if "); start >= 0 {
		cond = cond[start:]
	}
	for _, want := range []string{`${IMAGE}`, `${GO}`, `${DEPS}`} {
		if !strings.Contains(cond, want) {
			t.Errorf("run_image condition %q lacks %s: Go sources (**/*.go, GO filter) and go.mod/go.sum (DEPS filter) ship in the image", cond, want)
		}
	}
	image, ok := workflow.Jobs["image"]
	if !ok {
		t.Fatal("ci.yml has no image job")
	}
	if !strings.Contains(image.If, "run-image") {
		t.Errorf("job image if = %q, want the decide-script run-image output", image.If)
	}
}
