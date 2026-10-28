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

const (
	// oidcIssuerFlag is the exact issuer pin the signature check must carry.
	oidcIssuerFlag = `--certificate-oidc-issuer "https://token.actions.githubusercontent.com"`
	// identityFlag reads the expected signer identity from the step's env.
	identityFlag = `--certificate-identity "${CERTIFICATE_IDENTITY}"`
	tagStepName  = "Check the tag matches the manifest version"
)

var (
	pinnedAction    = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}$`)
	secretsPattern  = regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`)
	platformsLine   = regexp.MustCompile(`(?m)^[ \t-]*platforms:.*$`)
	cosignVerifyCmd = regexp.MustCompile(`(?m)^\s*cosign verify\s`)
	ghAttestVerify  = regexp.MustCompile(`(?m)^\s*gh attestation verify\s`)
)

// workflowStep is one step of a job, with the fields the release rules read.
type workflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
}

// workflowDoc is the part of a GitHub Actions workflow the release rules read.
type workflowDoc struct {
	On          map[string]any `yaml:"on"`
	Permissions any            `yaml:"permissions"`
	Jobs        map[string]struct {
		Needs any `yaml:"needs"`
		// If holds the job's `if:` condition. The image-lane test reads
		// it; the release rules ignore it.
		If    string         `yaml:"if"`
		Steps []workflowStep `yaml:"steps"`
	} `yaml:"jobs"`
}

// stepsRunning returns the steps of a job whose run script matches pattern.
func (d workflowDoc) stepsRunning(job string, pattern *regexp.Regexp) []workflowStep {
	var found []workflowStep
	for _, step := range d.Jobs[job].Steps {
		if pattern.MatchString(step.Run) {
			found = append(found, step)
		}
	}
	return found
}

// topLevelPermissionsAreReadOnly reports whether the workflow-wide permissions
// are exactly {contents: read}; a job adds its own scopes on top.
func topLevelPermissionsAreReadOnly(permissions any) bool {
	granted, ok := permissions.(map[string]any)
	return ok && len(granted) == 1 && granted["contents"] == "read"
}

// signatureCheckViolations reports how the keyless-signature check in the
// publish job is weaker than an exact issuer plus a workflow_ref identity.
func signatureCheckViolations(doc workflowDoc) []string {
	steps := doc.stepsRunning("publish", cosignVerifyCmd)
	if len(steps) == 0 {
		return []string{"publish: no cosign verify step"}
	}
	violations := []string{}
	for _, step := range steps {
		if !strings.Contains(step.Run, oidcIssuerFlag) {
			violations = append(violations, "publish: cosign verify does not pin the exact OIDC issuer")
		}
		if !strings.Contains(step.Run, identityFlag) {
			violations = append(violations, "publish: cosign verify does not read the identity from CERTIFICATE_IDENTITY")
		}
		identity := step.Env["CERTIFICATE_IDENTITY"]
		if !strings.HasPrefix(identity, "https://github.com/") || !strings.Contains(identity, "${{ github.workflow_ref }}") {
			violations = append(violations, "publish: CERTIFICATE_IDENTITY is not derived from github.workflow_ref")
		}
	}
	return violations
}

// jobNeeds returns the jobs a job waits for. GitHub Actions accepts a single
// name or a list, so both YAML shapes are read.
func (d workflowDoc) jobNeeds(job string) []string {
	switch needs := d.Jobs[job].Needs.(type) {
	case string:
		return []string{needs}
	case []any:
		names := make([]string, 0, len(needs))
		for _, entry := range needs {
			if name, ok := entry.(string); ok {
				names = append(names, name)
			}
		}
		return names
	}
	return nil
}

// publishWaitsForVerify reports whether the publish job needs the verify job,
// which holds the tag check and the tests. Without it, publish runs at once.
func publishWaitsForVerify(doc workflowDoc) bool {
	for _, name := range doc.jobNeeds("publish") {
		if name == "verify" {
			return true
		}
	}
	return false
}

// tagCheckViolations reports a verify job that does not compare the pushed tag
// with the manifest version before anything is built.
func tagCheckViolations(doc workflowDoc) []string {
	for _, step := range doc.Jobs["verify"].Steps {
		if step.Name == tagStepName && step.Env["TAG"] == "${{ github.ref_name }}" && strings.Contains(step.Run, "manifest.yaml") {
			return nil
		}
	}
	return []string{"verify: no step compares the pushed tag with the manifest version"}
}

// releaseWorkflowViolations reports how a workflow breaks the release rules:
// it publishes only for pushed v* tags, every action is pinned by full commit
// SHA, no secret other than GITHUB_TOKEN is read, nothing deploys, the
// workflow-wide permissions are read-only, the image is linux/amd64 only, the
// pushed tag is checked against the manifest version, and the signature and
// attestations are verified with an exact issuer and identity.
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
	if !topLevelPermissionsAreReadOnly(doc.Permissions) {
		violations = append(violations, "top-level permissions must be exactly contents: read")
	}
	platforms := platformsLine.FindAllString(string(raw), -1)
	if len(platforms) != 1 || strings.TrimLeft(platforms[0], " \t-") != "platforms: linux/amd64" {
		violations = append(violations, "the image must be built for exactly platforms: linux/amd64, got "+strings.Join(platforms, "|"))
	}
	violations = append(violations, tagCheckViolations(doc)...)
	if !publishWaitsForVerify(doc) {
		violations = append(violations, "publish: must list verify in needs, so the tag check and tests gate the push")
	}
	violations = append(violations, signatureCheckViolations(doc)...)
	if strings.Contains(string(raw), "-regexp") {
		violations = append(violations, "verification must not use a certificate regexp")
	}
	if len(doc.stepsRunning("publish", ghAttestVerify)) == 0 {
		violations = append(violations, "publish: no gh attestation verify step")
	}
	// continue-on-error turns a failed check into a pass, so no step or job may
	// carry it, whatever its value.
	if strings.Contains(string(raw), "continue-on-error") {
		violations = append(violations, "continue-on-error must not appear: a failed check would no longer stop the release")
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
	for _, want := range []string{"cosign sign --yes", "attest-build-provenance", "sbom-path", "packages: write", "id-token: write", "attestations: write"} {
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
	sha := "3d3c42e5aac5ba805825da76410c181273ba90b1"
	tests := map[string]string{
		"tag pin":          strings.Replace(good, "actions/checkout@"+sha, "actions/checkout@v5", 1),
		"short sha":        strings.Replace(good, "actions/checkout@"+sha, "actions/checkout@3d3c42e", 1),
		"pull request":     strings.Replace(good, "on:\n  push:", "on:\n  pull_request:\n  push:", 1),
		"branch push":      strings.Replace(good, "    tags:\n      - \"v*\"", "    branches: [main]\n    tags:\n      - \"v*\"", 1),
		"any tag":          strings.Replace(good, "- \"v*\"", "- \"*\"", 1),
		"extra secret":     strings.Replace(good, "secrets.GITHUB_TOKEN", "secrets.PUBLISH_KEY", 1),
		"deploy step":      good + "      - run: kubectl rollout restart deploy/x\n",
		"manual dispatch":  strings.Replace(good, "on:\n  push:", "on:\n  workflow_dispatch:\n  push:", 1),
		"unparseable yaml": "on: [",
		// The survivors of the delta review: each weakens the supply chain
		// while every older rule still passes.
		"cosign verify removed":  strings.Replace(good, `cosign verify "${IMAGE_REF}"`, `echo "${IMAGE_REF}"`, 1),
		"issuer regexp":          strings.Replace(good, oidcIssuerFlag, `--certificate-oidc-issuer-regexp ".*"`, 1),
		"issuer changed":         strings.Replace(good, "https://token.actions.githubusercontent.com", "https://accounts.example.invalid", 1),
		"identity not workflow":  strings.Replace(good, "github.workflow_ref", "github.ref", 1),
		"identity regexp":        strings.Replace(good, identityFlag, `--certificate-identity-regexp ".*"`, 1),
		"attestation verify off": strings.ReplaceAll(good, "gh attestation verify", "gh attestation download"),
		"write-all permissions":  strings.Replace(good, "permissions:\n  contents: read\n\nconcurrency", "permissions: write-all\n\nconcurrency", 1),
		"extra top permission":   strings.Replace(good, "permissions:\n  contents: read\n\nconcurrency", "permissions:\n  contents: read\n  packages: write\n\nconcurrency", 1),
		"second platform":        strings.Replace(good, "platforms: linux/amd64", "platforms: linux/amd64,linux/arm64", 1),
		"other platform":         strings.Replace(good, "platforms: linux/amd64", "platforms: linux/arm64", 1),
		"tag check removed":      strings.Replace(good, tagStepName, "Tag check", 1),
		"tag not from ref":       strings.Replace(good, "TAG: ${{ github.ref_name }}", "TAG: latest", 1),
		"verify step advisory":   strings.Replace(good, "      - name: Verify the signature\n", "      - name: Verify the signature\n        continue-on-error: true\n", 1),
		"tag check advisory":     strings.Replace(good, "      - name: Check the tag matches the manifest version\n", "      - name: Check the tag matches the manifest version\n        continue-on-error: true\n", 1),
		"publish job advisory":   strings.Replace(good, "  publish:\n", "  publish:\n    continue-on-error: true\n", 1),
		"publish needs removed":  strings.Replace(good, "    needs: verify\n", "", 1),
		"publish needs other":    strings.Replace(good, "needs: verify", "needs: [publish]", 1),
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

// TestPublishNeedsAcceptsEitherYAMLShape keeps the guard from rejecting a
// correct workflow that writes needs as a list.
func TestPublishNeedsAcceptsEitherYAMLShape(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(release.yml) error = %v", err)
	}
	listed := strings.Replace(string(raw), "needs: verify", "needs: [verify]", 1)
	if listed == string(raw) {
		t.Fatal("the planted change did not apply")
	}
	if violations := releaseWorkflowViolations([]byte(listed)); len(violations) != 0 {
		t.Errorf("a list-form needs on verify was rejected: %v", violations)
	}
}
