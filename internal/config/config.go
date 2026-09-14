// Package config parses trusted local RepoPlane startup configuration.
package config

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Settings struct {
	Workspace             string
	StateDir              string
	CatalogRoots          []string
	CandidateRoots        []string
	RuleFiles             []string
	EnableIntentionWrites bool
	EnableReportImport    bool
	EnableRunner          bool
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("path must not be empty")
	}
	*values = append(*values, value)
	return nil
}

func Parse(args []string, output io.Writer) (Settings, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return Settings{}, fmt.Errorf("get working directory: %w", err)
	}
	cacheDirectory, err := os.UserCacheDir()
	if err != nil {
		return Settings{}, fmt.Errorf("get user cache directory: %w", err)
	}
	settings := Settings{
		Workspace: workingDirectory,
		StateDir:  filepath.Join(cacheDirectory, "repoplane"),
	}
	var roots stringList
	var candidates stringList
	var ruleFiles stringList
	flags := flag.NewFlagSet("repoplane", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&settings.Workspace, "workspace", settings.Workspace, "trusted workspace root")
	flags.StringVar(&settings.StateDir, "state-dir", settings.StateDir, "local database and secret directory")
	flags.Var(&roots, "catalog-root", "workspace-relative catalog file or directory; repeatable")
	flags.Var(&candidates, "candidate-root", "workspace-relative executable candidate directory; repeatable")
	flags.Var(&ruleFiles, "rule-file", "rule filename discovered from workspace root to target; repeatable")
	flags.BoolVar(&settings.EnableIntentionWrites, "enable-intention-writes", false, "expose checkpoint and memo mutation tools")
	flags.BoolVar(&settings.EnableReportImport, "enable-report-import", false, "expose the local check-report importer")
	flags.BoolVar(&settings.EnableRunner, "enable-runner", false, "expose registered-capability prepare, execute, and inspect tools")
	if err := flags.Parse(args); err != nil {
		return Settings{}, err
	}
	if flags.NArg() != 0 {
		return Settings{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if len(roots) == 0 {
		roots = append(roots, "catalog")
	}
	settings.CatalogRoots = append([]string(nil), roots...)
	if len(candidates) == 0 {
		candidates = append(candidates, "tools", "scripts")
	}
	settings.CandidateRoots = append([]string(nil), candidates...)
	if len(ruleFiles) == 0 {
		ruleFiles = append(ruleFiles, "AGENTS.md")
	}
	settings.RuleFiles = append([]string(nil), ruleFiles...)
	return settings, nil
}
