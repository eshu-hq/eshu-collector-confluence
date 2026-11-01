// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// mainHeadStepName is the tag-only step that refuses a tag whose commit is not
// the protected main head.
const mainHeadStepName = "Require the tag to point at the protected main head"

// mainHeadViolations reports how the verify job's main-head guard is weaker
// than a tag-only step that runs after a credential-free checkout, compares
// github.sha with a freshly fetched origin/main, and precedes the manifest
// check. The publish job waits for verify, so this also gates the push.
func mainHeadViolations(doc workflowDoc) []string {
	verify := doc.Jobs["verify"]
	guardIdx, checkoutIdx, tagIdx := -1, -1, -1
	for i, step := range verify.Steps {
		switch {
		case step.Name == mainHeadStepName && guardIdx < 0:
			guardIdx = i
		case strings.HasPrefix(step.Uses, "actions/checkout@") && checkoutIdx < 0:
			checkoutIdx = i
		case step.Name == tagStepName && tagIdx < 0:
			tagIdx = i
		}
	}
	if guardIdx < 0 {
		return []string{fmt.Sprintf("verify: no step named %q", mainHeadStepName)}
	}
	violations := []string{}
	guard := verify.Steps[guardIdx]
	if guard.If != "github.ref_type == 'tag'" {
		violations = append(violations, fmt.Sprintf("verify: %q runs when %q, want only tag events", mainHeadStepName, guard.If))
	}
	if got := guard.Env["TAG_COMMIT"]; got != "${{ github.sha }}" {
		violations = append(violations, fmt.Sprintf("verify: %q TAG_COMMIT = %q, want ${{ github.sha }}", mainHeadStepName, got))
	}
	for _, want := range []string{"git fetch origin main", "git rev-parse origin/main", `"${TAG_COMMIT}" != "${main_head}"`, "exit 1"} {
		if !strings.Contains(guard.Run, want) {
			violations = append(violations, fmt.Sprintf("verify: %q lacks %q", mainHeadStepName, want))
		}
	}
	if got := verify.Env["TAG_NAME"]; got != "${{ github.ref_name }}" {
		violations = append(violations, fmt.Sprintf("verify: job env TAG_NAME = %q, want ${{ github.ref_name }}", got))
	}
	switch {
	case checkoutIdx < 0 || checkoutIdx > guardIdx:
		violations = append(violations, "verify: the main-head guard needs a checkout before it")
	case fmt.Sprint(verify.Steps[checkoutIdx].With["persist-credentials"]) != "false":
		violations = append(violations, "verify: the checkout before the main-head guard must set persist-credentials: false")
	}
	if tagIdx >= 0 && guardIdx > tagIdx {
		violations = append(violations, "verify: the main-head guard runs after the manifest version check")
	}
	return violations
}

func TestReleaseWorkflowRefusesTagsOffTheMainHead(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(release.yml) error = %v", err)
	}
	var doc workflowDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("release.yml is not valid YAML: %v", err)
	}
	if violations := mainHeadViolations(doc); len(violations) > 0 {
		t.Fatalf("release.yml main-head guard:\n%s", strings.Join(violations, "\n"))
	}
	if !publishWaitsForVerify(doc) {
		t.Fatal("publish must wait for verify, or the main-head guard does not gate the push")
	}
}

// TestReleaseMainHeadGuardCatchesViolations plants each way the guard can be
// weakened in a copy of the workflow, so a gate that always passes is caught.
func TestReleaseMainHeadGuardCatchesViolations(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(release.yml) error = %v", err)
	}
	good := string(raw)
	stepHead := "      - name: " + mainHeadStepName + "\n"
	tests := map[string]string{
		"guard renamed":             strings.Replace(good, mainHeadStepName, "Check the tag", 1),
		"guard runs on every event": strings.Replace(good, stepHead+"        if: github.ref_type == 'tag'\n", stepHead, 1),
		"wrong commit":              strings.Replace(good, "TAG_COMMIT: ${{ github.sha }}", "TAG_COMMIT: ${{ github.ref }}", 1),
		"main not fetched":          strings.Replace(good, "          git fetch origin main --quiet\n", "", 1),
		"guard never fails": strings.Replace(good, `not the protected main head ${main_head}" >&2
            exit 1`, `not the protected main head ${main_head}" >&2`, 1),
		"job env drops the tag name": strings.Replace(good, "      TAG_NAME: ${{ github.ref_name }}\n", "", 1),
		"checkout keeps credentials": strings.Replace(good, "persist-credentials: false", "persist-credentials: true", 1),
	}
	start := strings.Index(good, stepHead)
	end := strings.Index(good, "      - name: "+tagStepName)
	if start >= 0 && end > start {
		next := strings.Index(good[end:], "      - uses: actions/setup-go@")
		if next > 0 {
			next += end
			tests["guard after the manifest check"] = good[:start] + good[end:next] + good[start:end] + good[next:]
		}
	}
	if _, ok := tests["guard after the manifest check"]; !ok {
		t.Fatal("could not plant the reordering mutation")
	}
	for name, mutated := range tests {
		if mutated == good {
			t.Errorf("%s: the planted change did not apply", name)
			continue
		}
		var doc workflowDoc
		if err := yaml.Unmarshal([]byte(mutated), &doc); err != nil {
			t.Errorf("%s: mutated workflow does not parse: %v", name, err)
			continue
		}
		if len(mainHeadViolations(doc)) == 0 {
			t.Errorf("%s: the guard accepted a workflow that breaks the rules", name)
		}
	}
}

// gitIn runs git in dir and returns its trimmed output.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestReleaseMainHeadStepComparesTagCommitWithMain runs the workflow's own
// script the way the runner does (bash -e) in a clone of a scratch origin, so
// a tag cut from any commit other than the main head cannot publish.
func TestReleaseMainHeadStepComparesTagCommitWithMain(t *testing.T) {
	t.Parallel()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(release.yml) error = %v", err)
	}
	var doc workflowDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("release.yml is not valid YAML: %v", err)
	}
	var script string
	for _, step := range doc.Jobs["verify"].Steps {
		if step.Name == mainHeadStepName {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatalf("release.yml has no %q step", mainHeadStepName)
	}

	origin := t.TempDir()
	gitIn(t, origin, "init", "--quiet", "--initial-branch=main")
	gitIn(t, origin, "commit", "--quiet", "--allow-empty", "-m", "first")
	older := gitIn(t, origin, "rev-parse", "HEAD")
	gitIn(t, origin, "commit", "--quiet", "--allow-empty", "-m", "second")
	head := gitIn(t, origin, "rev-parse", "HEAD")
	gitIn(t, origin, "checkout", "--quiet", "-b", "feature")
	gitIn(t, origin, "commit", "--quiet", "--allow-empty", "-m", "unmerged")
	unmerged := gitIn(t, origin, "rev-parse", "HEAD")

	tests := []struct {
		name   string
		commit string
		want   bool
	}{
		{"tag at the main head", head, true},
		{"tag at an older main commit", older, false},
		{"tag at an unmerged commit", unmerged, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clone := t.TempDir()
			gitIn(t, clone, "clone", "--quiet", origin, ".")
			cmd := exec.Command(bash, "-e", "-c", script)
			cmd.Dir = clone
			cmd.Env = append(os.Environ(), "TAG_NAME=v9.9.9", "TAG_COMMIT="+tt.commit)
			out, err := cmd.CombinedOutput()
			if got := err == nil; got != tt.want {
				t.Fatalf("commit %s: passed = %t, want %t\n%s", tt.commit, got, tt.want, out)
			}
			if !tt.want && !strings.Contains(string(out), "not the protected main head") {
				t.Fatalf("refusal lacks the explanatory message:\n%s", out)
			}
		})
	}
}
