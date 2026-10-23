// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var (
	pinnedAction   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}$`)
	secretsPattern = regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`)
)

// workflowDoc is the part of a GitHub Actions workflow the release rules read.
type workflowDoc struct {
	On   map[string]any `yaml:"on"`
	Jobs map[string]struct {
		Steps []struct {
			Uses string `yaml:"uses"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// releaseWorkflowViolations reports how a workflow breaks the release rules:
// it publishes only for pushed v* tags, every action is pinned by full commit
// SHA, no secret other than GITHUB_TOKEN is read, and nothing deploys.
func releaseWorkflowViolations(raw []byte) []string {
	var doc workflowDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return []string{"workflow is not valid YAML: " + err.Error()}
	}
	violations := []string{}
	triggers := make([]string, 0, len(doc.On))
	for trigger := range doc.On {
		triggers = append(triggers, trigger)
	}
	sort.Strings(triggers)
	if len(triggers) != 1 || triggers[0] != "push" {
		violations = append(violations, "triggers must be exactly push, got "+strings.Join(triggers, ","))
	}
	if push, ok := doc.On["push"].(map[string]any); ok {
		if _, branches := push["branches"]; branches {
			violations = append(violations, "push must not filter on branches")
		}
		tags, _ := push["tags"].([]any)
		if len(tags) != 1 || tags[0] != "v*" {
			violations = append(violations, "push must trigger only on tags [v*]")
		}
	} else {
		violations = append(violations, "push trigger must filter on tags")
	}
	for name, job := range doc.Jobs {
		for _, step := range job.Steps {
			if step.Uses != "" && !pinnedAction.MatchString(step.Uses) {
				violations = append(violations, name+": action not pinned by full SHA: "+step.Uses)
			}
		}
	}
	for _, match := range secretsPattern.FindAllStringSubmatch(string(raw), -1) {
		if match[1] != "GITHUB_TOKEN" {
			violations = append(violations, "reads secret "+match[1])
		}
	}
	for _, word := range []string{"kubectl", "helm ", "argocd", "terraform", "aws "} {
		if strings.Contains(string(raw), word) {
			violations = append(violations, "looks like a deploy step: "+word)
		}
	}
	return violations
}

func TestReleaseWorkflowFollowsTheReleaseRules(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(release.yml) error = %v", err)
	}
	if violations := releaseWorkflowViolations(raw); len(violations) > 0 {
		t.Fatalf("release.yml breaks the release rules:\n%s", strings.Join(violations, "\n"))
	}
	for _, want := range []string{"platforms: linux/amd64", "cosign sign --yes", "attest-build-provenance", "sbom-path", "packages: write", "id-token: write", "attestations: write"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("release.yml lacks %q", want)
		}
	}
}

// TestReleaseWorkflowGuardCatchesViolations plants each violation in a copy of
// the workflow text, so a guard that always passes is caught.
func TestReleaseWorkflowGuardCatchesViolations(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(release.yml) error = %v", err)
	}
	good := string(raw)
	sha := "fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09"
	tests := map[string]string{
		"tag pin":          strings.Replace(good, "actions/checkout@"+sha, "actions/checkout@v5", 1),
		"short sha":        strings.Replace(good, "actions/checkout@"+sha, "actions/checkout@fbc6f39", 1),
		"pull request":     strings.Replace(good, "on:\n  push:", "on:\n  pull_request:\n  push:", 1),
		"branch push":      strings.Replace(good, "    tags:\n      - \"v*\"", "    branches: [main]\n    tags:\n      - \"v*\"", 1),
		"any tag":          strings.Replace(good, "- \"v*\"", "- \"*\"", 1),
		"extra secret":     strings.Replace(good, "secrets.GITHUB_TOKEN", "secrets.PUBLISH_KEY", 1),
		"deploy step":      good + "      - run: kubectl rollout restart deploy/x\n",
		"manual dispatch":  strings.Replace(good, "on:\n  push:", "on:\n  workflow_dispatch:\n  push:", 1),
		"unparseable yaml": "on: [",
	}
	if violations := releaseWorkflowViolations([]byte(good)); len(violations) != 0 {
		t.Fatalf("the real workflow already violates: %v", violations)
	}
	for name, mutated := range tests {
		if mutated == good {
			t.Errorf("%s: the planted change did not apply", name)
			continue
		}
		if len(releaseWorkflowViolations([]byte(mutated))) == 0 {
			t.Errorf("%s: the guard accepted a workflow that breaks the rules", name)
		}
	}
}
