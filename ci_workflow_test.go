// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"fmt"
	"os"
	"regexp"
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
	goCI, ok := workflow.Jobs["go-ci"]
	if !ok {
		t.Fatal("ci.yml has no go-ci job: lanes moved to the reusable workflow")
	}
	// The reusable lane bodies live in eshu-hq/.github, pinned by full
	// commit SHA with the major version in a comment, never a mutable tag.
	if !callerUsesRe.MatchString(goCI.Uses) {
		t.Errorf("job go-ci uses = %q, want the pinned go-collector-ci.yml reusable", goCI.Uses)
	}
	// Every lane the decide script gates must reach the reusable through
	// its run-* inputs, so a lane the caller forgets to wire never runs.
	// needs outputs are strings while the reusable declares booleans, so
	// the wiring must convert explicitly: a bare output string fails the
	// call before any lane starts.
	for _, lane := range []string{"lint", "test", "race", "boundary", "shell", "image", "govulncheck", "gosec"} {
		input, _ := goCI.With["run-"+lane].(string)
		if !strings.Contains(input, "needs.changes.outputs.run-"+lane+" == 'true'") {
			t.Errorf("go-ci run-%s = %q, want the decide-script run-%s output converted to boolean", lane, input, lane)
		}
	}
}

// callerUsesRe is the pinned reusable lane body: major-version comment,
// full commit SHA, never a mutable tag.
var callerUsesRe = regexp.MustCompile(`^eshu-hq/\.github/\.github/workflows/go-collector-ci\.yml@[0-9a-f]{40}$`)

// zeroPlaceholderDigest is the exact unreleased manifest digest the repo
// keeps by design; the deploy stamps the real one via seed --image-ref.
const zeroPlaceholderDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// callerContractViolations reports how the thin caller breaks the reusable
// contract: the required context, the fail-closed aggregator, the tool and
// coverage pins, the boundary and image hooks, the schedule-only gosec
// failure input, the SARIF re-upload, and the scheduled findings issue.
func callerContractViolations(doc workflowDoc) []string {
	violations := []string{}
	goCI, ok := doc.Jobs["go-ci"]
	if !ok {
		return []string{"ci.yml has no go-ci job"}
	}
	with := goCI.With
	if fmt.Sprintf("%v", with["coverage-floor"]) != "90" {
		violations = append(violations, fmt.Sprintf("go-ci coverage-floor = %v, want 90: the floor may only rise", with["coverage-floor"]))
	}
	if fmt.Sprintf("%v", with["gosec-version"]) != "v2.30.0" {
		violations = append(violations, fmt.Sprintf("go-ci gosec-version = %v, want the pinned v2.30.0", with["gosec-version"]))
	}
	if boundary, _ := with["boundary-command"].(string); !strings.Contains(boundary, "TestImportBoundary") {
		violations = append(violations, "go-ci boundary-command does not run the import-boundary gate")
	}
	imageCommand, _ := with["image-command"].(string)
	for _, want := range []string{"seed --dest", zeroPlaceholderDigest, "exit 1"} {
		if !strings.Contains(imageCommand, want) {
			violations = append(violations, fmt.Sprintf("go-ci image-command lacks %q: the seed contract and the zero-digest guard must ride the image lane", want))
		}
	}
	if tag, _ := with["image-tag"].(string); tag == "" {
		violations = append(violations, "go-ci image-tag is empty: the image lane must build the tagged image the seed contract validates")
	}
	if failOn, _ := with["gosec-fail-on-findings"].(string); !strings.Contains(failOn, "schedule") {
		violations = append(violations, "go-ci gosec-fail-on-findings does not answer to the schedule event: PRs stay advisory, scheduled findings fail")
	}
	if actionlint, _ := with["run-actionlint"].(string); !strings.Contains(actionlint, "run-lint") {
		violations = append(violations, "go-ci run-actionlint does not follow the lint lane: workflow changes must still lint the workflows")
	}
	complete, ok := doc.Jobs["ci-complete"]
	if !ok {
		violations = append(violations, "ci.yml has no ci-complete job: it stays the only required context")
	} else {
		needs := doc.jobNeeds("ci-complete")
		has := func(name string) bool {
			for _, need := range needs {
				if need == name {
					return true
				}
			}
			return false
		}
		// changes is listed so a failed path-selection job cannot
		// false-green; go-ci carries the lane results.
		if !has("changes") || !has("go-ci") {
			violations = append(violations, fmt.Sprintf("ci-complete needs = %v, want changes and go-ci", needs))
		}
		if complete.If != "always()" {
			violations = append(violations, fmt.Sprintf("ci-complete if = %q, want always()", complete.If))
		}
	}
	sarif, ok := doc.Jobs["gosec-sarif"]
	if !ok {
		violations = append(violations, "ci.yml has no gosec-sarif job: SARIF must still reach the Security tab")
	} else {
		if !strings.Contains(sarif.If, "pull_request") {
			violations = append(violations, fmt.Sprintf("gosec-sarif if = %q, want the non-PR guard so fork PRs stay read-only", sarif.If))
		}
		var sawDownload, sawUpload bool
		for _, step := range sarif.Steps {
			if strings.Contains(step.Uses, "actions/download-artifact@") {
				sawDownload = true
			}
			if strings.Contains(step.Uses, "github/codeql-action/upload-sarif@") {
				sawUpload = true
			}
		}
		if !sawDownload || !sawUpload {
			violations = append(violations, "gosec-sarif must download the gosec artifact and upload it to the Security tab")
		}
	}
	issue, ok := doc.Jobs["findings-issue"]
	if !ok {
		violations = append(violations, "ci.yml has no findings-issue job: scheduled failures must file or update an issue")
	} else {
		if !strings.Contains(issue.If, "failure()") || !strings.Contains(issue.If, "schedule") {
			violations = append(violations, fmt.Sprintf("findings-issue if = %q, want the failure-plus-schedule gate", issue.If))
		}
		var sawFile bool
		for _, step := range issue.Steps {
			if strings.Contains(step.Run, "gh issue create") {
				sawFile = true
			}
		}
		if !sawFile {
			violations = append(violations, "findings-issue has no step that files the issue")
		}
		if issue.Permissions["issues"] != "write" {
			violations = append(violations, "findings-issue needs issues: write")
		}
	}
	return violations
}

// TestReusableCallerContract pins the thin-caller shape after the lane
// bodies move to eshu-hq/.github: the required context name, the
// fail-closed aggregator, the pins and hooks, the SARIF re-upload, and
// the scheduled findings issue all live in the caller.
func TestReusableCallerContract(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow workflowDoc
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, violation := range callerContractViolations(workflow) {
		t.Error(violation)
	}
}
