// Package catalog loads and validates RepoPlane tool declarations.
package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

const MaxManifestBytes int64 = 1024 * 1024

var (
	ErrManifestTooLarge = errors.New("catalog: manifest exceeds size limit")
	ErrManifestInvalid  = errors.New("catalog: invalid manifest")
	idPattern           = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	argumentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

type Manifest struct {
	ID          string              `json:"id" yaml:"id"`
	Revision    uint64              `json:"revision" yaml:"revision"`
	Summary     string              `json:"summary" yaml:"summary"`
	UseWhen     []string            `json:"use_when,omitempty" yaml:"use_when,omitempty"`
	Tags        []string            `json:"tags,omitempty" yaml:"tags,omitempty"`
	Aliases     []string            `json:"aliases,omitempty" yaml:"aliases,omitempty"`
	Execution   *Execution          `json:"execution,omitempty" yaml:"execution,omitempty"`
	Arguments   map[string]Argument `json:"arguments,omitempty" yaml:"arguments,omitempty"`
	Inputs      []string            `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	Outputs     []string            `json:"outputs,omitempty" yaml:"outputs,omitempty"`
	Checks      []string            `json:"checks,omitempty" yaml:"checks,omitempty"`
	Docs        []string            `json:"docs,omitempty" yaml:"docs,omitempty"`
	CachePolicy string              `json:"cache_policy,omitempty" yaml:"cache_policy,omitempty"`
}

type Execution struct {
	Kind          string           `json:"kind" yaml:"kind"`
	ExecutableRef string           `json:"executable_ref" yaml:"executable_ref"`
	CWD           string           `json:"cwd" yaml:"cwd"`
	ArgvTemplate  []string         `json:"argv_template" yaml:"argv_template"`
	TrustedForRun bool             `json:"trusted_for_run" yaml:"trusted_for_run"`
	TimeoutSec    uint64           `json:"timeout_sec,omitempty" yaml:"timeout_sec,omitempty"`
	ArtifactMode  string           `json:"artifact_mode,omitempty" yaml:"artifact_mode,omitempty"`
	Preflight     []PreflightCheck `json:"preflight,omitempty" yaml:"preflight,omitempty"`
}

type PreflightCheck struct {
	ID          string   `json:"id" yaml:"id"`
	Kind        string   `json:"kind" yaml:"kind"`
	Ref         string   `json:"ref,omitempty" yaml:"ref,omitempty"`
	Requirement string   `json:"requirement,omitempty" yaml:"requirement,omitempty"`
	Argv        []string `json:"argv,omitempty" yaml:"argv,omitempty"`
}

type Argument struct {
	Type        string `json:"type" yaml:"type"`
	Required    bool   `json:"required,omitempty" yaml:"required,omitempty"`
	Default     any    `json:"default,omitempty" yaml:"default,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// Decode reads exactly one strict YAML or JSON document from a bounded reader.
func Decode(r io.Reader, format string) (Manifest, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	if int64(len(data)) > MaxManifestBytes {
		return Manifest{}, ErrManifestTooLarge
	}

	var manifest Manifest
	switch strings.ToLower(format) {
	case ".json", "json":
		err = decodeJSON(data, &manifest)
	case ".yaml", ".yml", "yaml", "yml":
		err = decodeYAML(data, &manifest)
	default:
		err = fmt.Errorf("unsupported manifest format %q", format)
	}
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrManifestInvalid, err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrManifestInvalid, err)
	}
	return manifest, nil
}

func decodeJSON(data []byte, target *Manifest) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeYAML(data []byte, target *Manifest) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil && extra != nil {
			return errors.New("multiple YAML documents")
		}
		return err
	}
	return nil
}

func (m Manifest) Validate() error {
	if !idPattern.MatchString(m.ID) {
		return errors.New("id must match ^[a-z0-9][a-z0-9._-]{0,127}$")
	}
	if m.Revision == 0 {
		return errors.New("revision must be greater than zero")
	}
	if strings.TrimSpace(m.Summary) == "" {
		return errors.New("summary is required")
	}
	if m.CachePolicy != "" && m.CachePolicy != "disabled" {
		return errors.New("MVP only supports cache_policy=disabled")
	}
	if m.Execution != nil && m.Execution.Kind != "cli" {
		return errors.New("MVP only recognizes execution.kind=cli")
	}
	if m.Execution != nil {
		if strings.TrimSpace(m.Execution.ExecutableRef) == "" {
			return errors.New("execution.executable_ref is required")
		}
		if len(m.Execution.ArgvTemplate) > 256 || m.Execution.TimeoutSec > 24*60*60 {
			return errors.New("execution limits exceed supported bounds")
		}
		if m.Execution.ArtifactMode != "" && m.Execution.ArtifactMode != "metadata" && m.Execution.ArtifactMode != "capture" {
			return errors.New("execution.artifact_mode must be metadata or capture")
		}
		if len(m.Execution.Preflight) > 64 {
			return errors.New("execution.preflight exceeds supported bounds")
		}
		seen := make(map[string]struct{}, len(m.Execution.Preflight))
		for _, check := range m.Execution.Preflight {
			if !idPattern.MatchString(check.ID) {
				return errors.New("preflight id is invalid")
			}
			if _, exists := seen[check.ID]; exists {
				return errors.New("preflight id must be unique")
			}
			seen[check.ID] = struct{}{}
			if check.Kind != "executable" && check.Kind != "file" && check.Kind != "environment" && check.Kind != "git" && check.Kind != "platform" {
				return errors.New("preflight kind is unsupported")
			}
			if check.Requirement != "" && check.Requirement != "required" && check.Requirement != "recommended" && check.Requirement != "informational" {
				return errors.New("preflight requirement is unsupported")
			}
			if len(check.Ref) > 4096 || len(check.Argv) > 32 {
				return errors.New("preflight declaration exceeds supported bounds")
			}
			if len(check.Argv) > 0 && check.Kind != "executable" {
				return errors.New("preflight argv is only valid for executable checks")
			}
		}
	}
	if len(m.Arguments) > 64 || len(m.Inputs) > 128 || len(m.Outputs) > 128 || len(m.Checks) > 128 || len(m.Docs) > 128 {
		return errors.New("manifest collections exceed supported bounds")
	}
	for name, argument := range m.Arguments {
		if !argumentNamePattern.MatchString(name) {
			return errors.New("argument name is invalid")
		}
		if argument.Type != "string" && argument.Type != "project_path" && argument.Type != "boolean" && argument.Type != "bool" && argument.Type != "integer" {
			return errors.New("argument type is unsupported")
		}
		if len(argument.Description) > 4096 {
			return errors.New("argument description exceeds supported bounds")
		}
	}
	return nil
}
