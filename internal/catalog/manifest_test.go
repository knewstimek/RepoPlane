package catalog

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodeYAML(t *testing.T) {
	const input = `
id: schema.validate
revision: 1
summary: validate configuration
tags: [config, validation]
execution:
  kind: cli
  executable_ref: tools.validator
  cwd: repository
  argv_template: ["--input", "{input}"]
  trusted_for_run: false
arguments:
  input:
    type: project_path
    required: true
cache_policy: disabled
`
	got, err := Decode(strings.NewReader(input), "yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "schema.validate" || got.Arguments["input"].Type != "project_path" {
		t.Fatalf("unexpected manifest: %+v", got)
	}
}

func TestDecodeRejectsUnknownField(t *testing.T) {
	const input = `id: tool.example
revision: 1
summary: example
unexpected: true
`
	_, err := Decode(strings.NewReader(input), "yaml")
	if !errors.Is(err, ErrManifestInvalid) {
		t.Fatalf("error = %v, want ErrManifestInvalid", err)
	}
}

func TestDecodeRejectsOversizedManifest(t *testing.T) {
	_, err := Decode(strings.NewReader(strings.Repeat("x", int(MaxManifestBytes+1))), "json")
	if !errors.Is(err, ErrManifestTooLarge) {
		t.Fatalf("error = %v, want ErrManifestTooLarge", err)
	}
}

func TestValidateRequiredFields(t *testing.T) {
	tests := []Manifest{
		{Revision: 1, Summary: "summary"},
		{ID: "UPPER", Revision: 1, Summary: "summary"},
		{ID: "valid", Summary: "summary"},
		{ID: "valid", Revision: 1},
	}
	for _, manifest := range tests {
		if err := manifest.Validate(); err == nil {
			t.Fatalf("Validate(%+v) succeeded, want error", manifest)
		}
	}
}

func TestDecodeExecutionPolicyIsAdditive(t *testing.T) {
	const input = `
id: build.test
revision: 2
summary: run tests
execution:
  kind: cli
  executable_ref: go
  cwd: .
  argv_template: [test, ./...]
  trusted_for_run: true
  timeout_sec: 300
  artifact_mode: metadata
  preflight:
    - id: go.version
      kind: executable
      ref: go
      requirement: required
      argv: [version]
`
	got, err := Decode(strings.NewReader(input), "yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got.Execution == nil || got.Execution.TimeoutSec != 300 || len(got.Execution.Preflight) != 1 {
		t.Fatalf("unexpected execution policy: %+v", got.Execution)
	}
}
