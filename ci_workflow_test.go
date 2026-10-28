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
// run_image assignment must answer to both with positive polarity.
// A bare ${GO} substring match would pass a negated condition such as
// "! is_true", so the test asserts the is_true clauses and rejects
// their negations, mirroring the terraform-state/cicd-run hardened
// guards.
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
	for _, want := range []string{`${IMAGE}`, `is_true "${GO}"`, `is_true "${DEPS}"`} {
		if !strings.Contains(cond, want) {
			t.Errorf("run_image condition %q lacks %s: Go sources (**/*.go, GO filter) and go.mod/go.sum (DEPS filter) ship in the image", cond, want)
		}
	}
	for _, bad := range []string{`! is_true "${GO}"`, `! is_true "${DEPS}"`} {
		if strings.Contains(cond, bad) {
			t.Errorf("run_image condition %q contains inverted %s: negated GO/DEPS would skip the image lane on Go-source PRs", cond, bad)
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

// scheduledFindingsViolations reports how the govulncheck and gosec jobs
// break the scheduled-findings contract: each must carry issues:write (so a
// scheduled run can file or update a findings issue with the default
// GITHUB_TOKEN), must run a filing step gated on failure plus the schedule
// event that creates or comments on the issue, and gosec must additionally
// count its SARIF findings and fail a scheduled run that has any — PR runs
// stay advisory. Without the count-and-fail step, the filing step is
// unreachable and scheduled findings stay silent.
func scheduledFindingsViolations(doc workflowDoc) []string {
	violations := []string{}
	for _, job := range []string{"govulncheck", "gosec"} {
		def, ok := doc.Jobs[job]
		if !ok {
			return []string{"ci.yml has no " + job + " job"}
		}
		if def.Permissions["issues"] != "write" {
			violations = append(violations, job+": needs issues: write to file scheduled findings")
		}
		filing := false
		for _, step := range def.Steps {
			if strings.Contains(step.Run, "gh issue create") &&
				strings.Contains(step.If, "failure()") &&
				strings.Contains(step.If, "schedule") {
				filing = true
			}
		}
		if !filing {
			violations = append(violations, job+": no filing step gated on failure() and the schedule event")
		}
	}
	counts := false
	for _, step := range doc.Jobs["gosec"].Steps {
		if strings.Contains(step.Run, "gosec findings") &&
			strings.Contains(step.Run, "exit 1") &&
			strings.Contains(step.Run, "schedule") {
			counts = true
		}
	}
	if !counts {
		violations = append(violations, "gosec: no findings-count step that fails a scheduled run with findings")
	}
	return violations
}

// TestScheduledFindingsFileIssues pins the weekly-schedule half of the org
// gate contract: govulncheck and gosec findings open or update an issue.
// The decide script already routes schedule events to both lanes; this test
// pins the filing side — permissions, the failure-plus-schedule gate, and
// the gosec count that makes a scheduled failure (and only one) reach it.
func TestScheduledFindingsFileIssues(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow workflowDoc
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, violation := range scheduledFindingsViolations(workflow) {
		t.Error(violation)
	}
}

// manifestGuardViolations reports an image lane that lets the committed
// manifest look pinned: the repository copy must stay the exact zero
// placeholder (an image cannot name its own digest; the deploy stamps the
// real one via seed --image-ref). Any other value — a stale or partial
// digest — must fail the lane instead of shipping a manifest that looks
// pinned but is not.
func manifestGuardViolations(doc workflowDoc) []string {
	image, ok := doc.Jobs["image"]
	if !ok {
		return []string{"ci.yml has no image job"}
	}
	for _, step := range image.Steps {
		if strings.Contains(step.Run, "sha256:0000000000000000000000000000000000000000000000000000000000000000") &&
			strings.Contains(step.Run, "exit 1") {
			return nil
		}
	}
	return []string{"image: no guard step failing the lane unless the manifest keeps the exact zero placeholder digest"}
}

// TestManifestUnreleasedStateFailsLoudly pins the epic's manifest child:
// the committed manifest keeps the zero placeholder by design, and the
// image lane fails loudly on anything else rather than letting a
// half-pinned manifest look releasable.
func TestManifestUnreleasedStateFailsLoudly(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow workflowDoc
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, violation := range manifestGuardViolations(workflow) {
		t.Error(violation)
	}
}
