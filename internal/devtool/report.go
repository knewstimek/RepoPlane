// Package devtool implements bounded, local developer checks. It is not an MCP runner.
package devtool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const SchemaVersion = "check-report.v1"

type Report struct {
	SchemaVersion string    `json:"schema_version"`
	Operation     string    `json:"operation"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	Subject       Subject   `json:"subject"`
	Checks        []Check   `json:"checks"`
	Counts        Counts    `json:"counts"`
	Truncated     bool      `json:"truncated"`
}

type Subject struct {
	GitCommit string `json:"git_commit,omitempty"`
	Dirty     *bool  `json:"dirty,omitempty"`
}

type Check struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	DurationMS  int64  `json:"duration_ms"`
	Summary     string `json:"summary,omitempty"`
	Diagnostics string `json:"diagnostics,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

type Counts struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Unknown int `json:"unknown"`
}

func (r *Report) Finish() {
	r.FinishedAt = time.Now().UTC()
	r.Status = "passed"
	for _, check := range r.Checks {
		switch check.Status {
		case "passed":
			r.Counts.Passed++
		case "failed":
			r.Counts.Failed++
		default:
			r.Counts.Unknown++
		}
		r.Truncated = r.Truncated || check.Truncated
	}
	if r.Counts.Failed > 0 {
		r.Status = "failed"
	} else if r.Counts.Unknown > 0 || r.Truncated {
		r.Status = "partial"
	}
}

func WriteReport(path string, report Report) error {
	if path == "" {
		return errors.New("report output path is required")
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	data = append(data, '\n')
	if len(data) > 1024*1024 {
		return errors.New("report exceeds 1 MiB limit")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".report-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary report: %w", err)
	}
	temporaryName := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect temporary report: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary report: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary report: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("replace report: %w", err)
		}
		if err := os.Rename(temporaryName, path); err != nil {
			return fmt.Errorf("replace report: %w", err)
		}
	}
	keep = true
	return nil
}

func sanitizeDiagnostic(value, root string) string {
	value = replaceLocalPath(value, root, "WORKSPACE")
	value = replaceLocalPath(value, os.Getenv("GOTMPDIR"), "GO_TMP")
	value = replaceLocalPath(value, os.TempDir(), "TEMP")
	if home, err := os.UserHomeDir(); err == nil {
		value = replaceLocalPath(value, home, "USER_HOME")
	}
	value = strings.TrimSpace(value)
	if len(value) > 8192 {
		return value[len(value)-8192:]
	}
	return value
}

func replaceLocalPath(value, path, replacement string) string {
	if path == "" {
		return value
	}
	value = strings.ReplaceAll(value, filepath.Clean(path), replacement)
	value = strings.ReplaceAll(value, filepath.ToSlash(filepath.Clean(path)), replacement)
	return value
}
