// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// activationFile is the shape of the component activation config file the
// extension worker reads: the optional host claim identity, the process
// runner, and the collector's own config.
type activationFile struct {
	Host struct {
		SourceSystem string `yaml:"sourceSystem"`
		Scope        struct {
			ID   string `yaml:"id"`
			Kind string `yaml:"kind"`
		} `yaml:"scope"`
	} `yaml:"host"`
	Process struct {
		Command          string   `yaml:"command"`
		Args             []string `yaml:"args"`
		StdoutLimitBytes int64    `yaml:"stdout_limit_bytes"`
		StderrLimitBytes int64    `yaml:"stderr_limit_bytes"`
	} `yaml:"process"`
	Config map[string]any `yaml:"config"`
}

func loadActivationExample(t *testing.T) activationFile {
	t.Helper()
	raw, err := os.ReadFile("config.example.yaml")
	if err != nil {
		t.Fatalf("os.ReadFile(config.example.yaml) error = %v", err)
	}
	var file activationFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatalf("yaml.Unmarshal(config.example.yaml) error = %v", err)
	}
	return file
}

// TestConfigExampleIsALoadableActivation proves the checked-in example parses
// through the collector's own strict config reader and names the identity the
// host plans claims against.
func TestConfigExampleIsALoadableActivation(t *testing.T) {
	t.Parallel()

	file := loadActivationExample(t)
	cfg, err := ParseConfig(file.Config, envFrom(basicEnv))
	if err != nil {
		t.Fatalf("ParseConfig(config.example.yaml) error = %v", err)
	}
	if cfg.SpaceID != "12345" || cfg.BaseURL == "" {
		t.Errorf("config = %+v", cfg)
	}
	if file.Host.SourceSystem != SourceSystem || file.Host.Scope.Kind != "documentation_source" ||
		file.Host.Scope.ID != "doc-source:confluence:confluence.example.invalid:12345" {
		t.Errorf("host = %+v, want the in-tree scope id shape", file.Host)
	}
	if file.Process.Command != "./collector" || len(file.Process.Args) != 1 || file.Process.Args[0] != "--sdk-stdio" {
		t.Errorf("process = %+v", file.Process)
	}
	if int64(cfg.MaxResultBytes) >= file.Process.StdoutLimitBytes {
		t.Errorf("max_result_bytes %d must stay below process.stdout_limit_bytes %d", cfg.MaxResultBytes, file.Process.StdoutLimitBytes)
	}
}

// TestConfigExampleHoldsNoSecretOrRealHost keeps real values out of the
// repository: the example may name environment variables, never values.
func TestConfigExampleHoldsNoSecretOrRealHost(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("config.example.yaml")
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	cfg := loadActivationExample(t).Config
	if _, err := ParseConfig(cfg, envFrom(map[string]string{})); err == nil {
		t.Fatal("the example resolved credentials with an empty environment")
	}
	for _, forbidden := range []string{"atlassian.net", "@", "Bearer ", "Basic "} {
		if containsOutsideComments(string(raw), forbidden) {
			t.Errorf("config.example.yaml contains %q outside a comment", forbidden)
		}
	}
}

func containsOutsideComments(text, needle string) bool {
	for _, line := range strings.Split(text, "\n") {
		if hash := strings.IndexByte(line, '#'); hash >= 0 {
			line = line[:hash]
		}
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}
