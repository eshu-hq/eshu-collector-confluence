// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"bytes"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
	"github.com/eshu-hq/eshu/sdk/go/collector/conformance"
)

var digestPattern = regexp.MustCompile(`@sha256:[a-f0-9]{64}$`)

func loadManifest(t *testing.T) conformance.Manifest {
	t.Helper()
	raw, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatalf("os.ReadFile(manifest.yaml) error = %v", err)
	}
	var manifest conformance.Manifest
	if err := yaml.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("yaml.Unmarshal(manifest.yaml) error = %v", err)
	}
	return manifest
}

func TestEmbeddedManifestIsTheCheckedInManifest(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatalf("os.ReadFile(manifest.yaml) error = %v", err)
	}
	if !bytes.Equal(raw, ManifestYAML) {
		t.Fatal("ManifestYAML differs from manifest.yaml")
	}
}

// TestManifestDeclaresExactlyWhatTheCollectorEmits pins the manifest to the
// code: the same fact kinds, schema versions, and confidence the contract
// validator enforces, each with a payload schema reference.
func TestManifestDeclaresExactlyWhatTheCollectorEmits(t *testing.T) {
	t.Parallel()

	manifest := loadManifest(t)
	if manifest.Metadata.ID != ComponentID {
		t.Errorf("metadata.id = %q, want %q", manifest.Metadata.ID, ComponentID)
	}
	if !reflect.DeepEqual(manifest.Spec.CollectorKinds, []string{CollectorKind}) {
		t.Errorf("collectorKinds = %v, want [%s]", manifest.Spec.CollectorKinds, CollectorKind)
	}
	if manifest.Spec.Runtime.SDKProtocol != sdk.ProtocolVersionV1Alpha1 || manifest.Spec.Runtime.Adapter != "process" {
		t.Errorf("runtime = %+v, want collector-sdk/v1alpha1 on the process adapter", manifest.Spec.Runtime)
	}
	if manifest.Spec.ComponentType != "collector" {
		t.Errorf("componentType = %q, want collector", manifest.Spec.ComponentType)
	}
	if manifest.Spec.Telemetry.MetricsPrefix != MetricsPrefix {
		t.Errorf("metricsPrefix = %q, want %q", manifest.Spec.Telemetry.MetricsPrefix, MetricsPrefix)
	}
	if !reflect.DeepEqual(manifest.Contract(), Contract()) {
		t.Errorf("manifest contract differs from Contract():\n manifest %+v\n code     %+v", manifest.Contract(), Contract())
	}
	for _, family := range manifest.Spec.EmittedFacts {
		if family.PayloadSchemaRef != family.Kind {
			t.Errorf("%s payloadSchemaRef = %q, want the kind's own schema", family.Kind, family.PayloadSchemaRef)
		}
	}
}

// TestManifestReleaseProvenance proves the manifest is releasable: every
// artifact is digest-pinned, the core range and wire protocol are declared, and
// every fact family is versioned with declared confidence. The all-zero digest
// is a placeholder: the image cannot name its own digest, so `collector seed
// --image-ref` stamps the real one into the seeded manifest.
func TestManifestReleaseProvenance(t *testing.T) {
	t.Parallel()

	manifest := loadManifest(t)
	if strings.TrimSpace(manifest.APIVersion) == "" || strings.TrimSpace(manifest.Kind) == "" {
		t.Fatal("manifest missing apiVersion/kind")
	}
	if strings.TrimSpace(manifest.Metadata.Version) == "" || strings.TrimSpace(manifest.Metadata.Publisher) == "" {
		t.Fatal("manifest missing version or publisher")
	}
	if len(manifest.Spec.Artifacts) == 0 {
		t.Fatal("manifest declares no artifacts")
	}
	for _, artifact := range manifest.Spec.Artifacts {
		if !digestPattern.MatchString(artifact.Image) {
			t.Fatalf("artifact image %q is not digest-pinned", artifact.Image)
		}
		if !strings.HasPrefix(artifact.Image, "ghcr.io/eshu-hq/eshu-collector-confluence@") {
			t.Errorf("artifact image %q is not the collector image", artifact.Image)
		}
	}
	if len(manifest.Spec.EmittedFacts) != 4 {
		t.Fatalf("emitted facts = %d, want the four documentation kinds", len(manifest.Spec.EmittedFacts))
	}
	for _, family := range manifest.Spec.EmittedFacts {
		if len(family.SchemaVersions) == 0 || len(family.SourceConfidence) == 0 {
			t.Fatalf("fact kind %q lacks schema versions or source confidence", family.Kind)
		}
	}
	if !reflect.DeepEqual(manifest.Spec.ConsumerContracts.Reducer.Phases, []string{conformance.SourceEvidenceOnlyReducerPhase}) {
		t.Errorf("reducer phases = %v, want only %q", manifest.Spec.ConsumerContracts.Reducer.Phases, conformance.SourceEvidenceOnlyReducerPhase)
	}
}

// TestDockerfileBuildsTheManifestPlatform ties the image to the manifest. The
// artifact declares linux/amd64, so the Dockerfile must not depend on the
// builder's architecture: it pins the target of the static build and the
// platform of the final stage.
func TestDockerfileBuildsTheManifestPlatform(t *testing.T) {
	t.Parallel()

	for _, artifact := range loadManifest(t).Spec.Artifacts {
		if artifact.Platform != "linux/amd64" {
			t.Fatalf("artifact platform = %q, this test and the Dockerfile assume linux/amd64", artifact.Platform)
		}
	}
	raw, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatalf("os.ReadFile(Dockerfile) error = %v", err)
	}
	dockerfile := string(raw)
	for _, want := range []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "FROM --platform=linux/amd64 gcr.io/distroless/"} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
}
