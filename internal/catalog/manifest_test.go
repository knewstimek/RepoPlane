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

func TestDecodeMarkdownFrontmatterAndRejectAlias(t *testing.T) {
	input := "---\nid: docs.tool\nrevision: 1\nsummary: documented tool\ntags: [docs]\n---\n# Body\n"
	got, err := Decode(strings.NewReader(input), ".md")
	if err != nil || got.ID != "docs.tool" {
		t.Fatalf("manifest=%+v err=%v", got, err)
	}
	_, err = Decode(strings.NewReader("---\nid: alias.tool\nrevision: 1\nsummary: &s text\ntags: [*s]\n---\n"), ".md")
	if !errors.Is(err, ErrManifestInvalid) {
		t.Fatalf("alias error=%v", err)
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

func TestDecodeHintsWhenFlowMappingCommaCreatesUnknownField(t *testing.T) {
	const input = `id: tool.example
revision: 1
summary: example
arguments: {target: {type: string, description: deploy frontend, gateway: optional}}
`
	_, err := Decode(strings.NewReader(input), "yaml")
	if !errors.Is(err, ErrManifestInvalid) || !strings.Contains(err.Error(), "quote scalar values that contain commas") {
		t.Fatalf("error = %v, want flow-style comma quoting hint", err)
	}
}

func TestDecodeDoesNotSuggestCommaForBlockUnknownField(t *testing.T) {
	const input = `id: tool.example
revision: 1
summary: example
tags: [test]
unexpected: true
`
	_, err := Decode(strings.NewReader(input), "yaml")
	if !errors.Is(err, ErrManifestInvalid) || strings.Contains(err.Error(), "quote scalar values") {
		t.Fatalf("error = %v, want ordinary unknown-field diagnostic", err)
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

func TestDecodeAcceptsQualifiedCacheManifest(t *testing.T) {
	manifest := `id: schema.output
revision: 2
summary: deterministic transform
execution:
  kind: cli
  executable_ref: tool
  cwd: .
  argv_template: [build]
  trusted_for_run: true
  artifact_mode: capture
  preflight:
    - {id: runtime.version, kind: executable, ref: tool, requirement: required, argv: [version]}
inputs: [src.txt]
outputs: [dist/out.txt]
cache_policy: verified
cache:
  contract_revision: 1
  output_contract: schema-output.v1
  key_checks: [runtime.version]
  qualification_checks: [cache.schema-output.differential]
  restore_policy: missing_or_matching
  assumptions:
    inputs_complete: true
    outputs_complete: true
    external_state: none
    nondeterminism: none
    side_effects: declared_outputs_only
`
	got, err := Decode(strings.NewReader(manifest), ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cache == nil || got.Cache.ContractRevision != 1 || got.CachePolicy != "verified" {
		t.Fatalf("unexpected cache declaration: %+v", got.Cache)
	}
}

func TestDecodeRejectsUnsafeCacheManifest(t *testing.T) {
	manifest := `id: unsafe.cache
revision: 1
summary: incomplete transform
execution: {kind: cli, executable_ref: tool, cwd: ., argv_template: [build], trusted_for_run: true, artifact_mode: metadata}
inputs: [src.txt]
outputs: [out.txt]
cache_policy: verified
cache:
  contract_revision: 1
  output_contract: output.v1
  key_checks: [runtime.version]
  qualification_checks: [cache.differential]
  restore_policy: missing_or_matching
  assumptions: {inputs_complete: true, outputs_complete: true, external_state: none, nondeterminism: none, side_effects: declared_outputs_only}
`
	if _, err := Decode(strings.NewReader(manifest), ".yaml"); !errors.Is(err, ErrManifestInvalid) {
		t.Fatalf("error=%v, want ErrManifestInvalid", err)
	}
}
