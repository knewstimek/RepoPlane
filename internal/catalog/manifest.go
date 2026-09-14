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
	Kind          string   `json:"kind" yaml:"kind"`
	ExecutableRef string   `json:"executable_ref" yaml:"executable_ref"`
	CWD           string   `json:"cwd" yaml:"cwd"`
	ArgvTemplate  []string `json:"argv_template" yaml:"argv_template"`
	TrustedForRun bool     `json:"trusted_for_run" yaml:"trusted_for_run"`
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
	return nil
}
