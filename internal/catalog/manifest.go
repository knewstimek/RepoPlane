// Package catalog loads and validates RepoPlane tool declarations.
package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
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
	Cache       *Cache              `json:"cache,omitempty" yaml:"cache,omitempty"`
}

type Cache struct {
	ContractRevision    uint64           `json:"contract_revision" yaml:"contract_revision"`
	OutputContract      string           `json:"output_contract" yaml:"output_contract"`
	KeyChecks           []string         `json:"key_checks,omitempty" yaml:"key_checks,omitempty"`
	QualificationChecks []string         `json:"qualification_checks,omitempty" yaml:"qualification_checks,omitempty"`
	RestorePolicy       string           `json:"restore_policy" yaml:"restore_policy"`
	IsolatedRoot        string           `json:"isolated_root,omitempty" yaml:"isolated_root,omitempty"`
	Assumptions         CacheAssumptions `json:"assumptions" yaml:"assumptions"`
}

type CacheAssumptions struct {
	InputsComplete  bool   `json:"inputs_complete" yaml:"inputs_complete"`
	OutputsComplete bool   `json:"outputs_complete" yaml:"outputs_complete"`
	ExternalState   string `json:"external_state" yaml:"external_state"`
	Nondeterminism  string `json:"nondeterminism" yaml:"nondeterminism"`
	SideEffects     string `json:"side_effects" yaml:"side_effects"`
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
	if m.CachePolicy != "" && m.CachePolicy != "disabled" && m.CachePolicy != "observe" && m.CachePolicy != "verified" {
		return errors.New("cache_policy must be disabled, observe, or verified")
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
	if err := m.validateCache(); err != nil {
		return err
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

func (m Manifest) validateCache() error {
	policy := m.CachePolicy
	if policy == "" {
		policy = "disabled"
	}
	if policy == "disabled" {
		if m.Cache != nil {
			return errors.New("cache configuration requires cache_policy=observe or verified")
		}
		return nil
	}
	if m.Cache == nil || m.Execution == nil || !m.Execution.TrustedForRun || m.Execution.ArtifactMode != "capture" {
		return errors.New("cache requires trusted execution with artifact_mode=capture")
	}
	if len(m.Inputs) == 0 || len(m.Outputs) == 0 || m.Cache.ContractRevision == 0 || !idPattern.MatchString(m.Cache.OutputContract) {
		return errors.New("cache requires inputs, outputs, contract_revision, and a valid output_contract")
	}
	if m.Cache.RestorePolicy != "missing_or_matching" && m.Cache.RestorePolicy != "replace_isolated_root" {
		return errors.New("cache restore_policy is unsupported")
	}
	if m.Cache.RestorePolicy == "replace_isolated_root" {
		root := filepath.Clean(filepath.FromSlash(m.Cache.IsolatedRoot))
		if m.Cache.IsolatedRoot == "" || filepath.IsAbs(root) || root == "." || root == ".." || strings.HasPrefix(root, ".."+string(filepath.Separator)) || strings.ContainsAny(m.Cache.IsolatedRoot, "*?") {
			return errors.New("replace_isolated_root requires a fixed workspace-relative isolated_root")
		}
		prefix := strings.TrimSuffix(filepath.ToSlash(m.Cache.IsolatedRoot), "/") + "/"
		for _, output := range m.Outputs {
			if !strings.HasPrefix(filepath.ToSlash(output), prefix) {
				return errors.New("all outputs must be below isolated_root")
			}
		}
	}
	a := m.Cache.Assumptions
	if !a.InputsComplete || !a.OutputsComplete || a.ExternalState != "none" || a.Nondeterminism != "none" || a.SideEffects != "declared_outputs_only" {
		return errors.New("cache purity assumptions are incomplete")
	}
	if len(m.Cache.KeyChecks) == 0 || len(m.Cache.KeyChecks) > 64 || len(m.Cache.QualificationChecks) > 64 || (policy == "verified" && len(m.Cache.QualificationChecks) == 0) {
		return errors.New("cache check declarations are incomplete or exceed limits")
	}
	preflight := make(map[string]PreflightCheck, len(m.Execution.Preflight))
	for _, check := range m.Execution.Preflight {
		preflight[check.ID] = check
	}
	for _, id := range m.Cache.KeyChecks {
		check, ok := preflight[id]
		if !ok || check.Kind != "executable" || check.Requirement != "required" {
			return errors.New("cache key_checks must reference required executable preflight checks")
		}
	}
	for _, id := range m.Cache.QualificationChecks {
		if !idPattern.MatchString(id) {
			return errors.New("cache qualification check id is invalid")
		}
	}
	return nil
}
