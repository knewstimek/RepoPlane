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
	Transport             string
	HTTPProfile           string
	Workspace             string
	StateDir              string
	CatalogRoots          []string
	CandidateRoots        []string
	RuleFiles             []string
	SymbolIndexes         []string
	EnableIntentionWrites bool
	EnableReportImport    bool
	EnableRunner          bool
	EnableCache           bool
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
	var symbolIndexes stringList
	flags := flag.NewFlagSet("repoplane", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&settings.Workspace, "workspace", settings.Workspace, "trusted workspace root")
	flags.StringVar(&settings.Transport, "transport", "stdio", "MCP transport: stdio or http")
	flags.StringVar(&settings.HTTPProfile, "http-profile", "", "ignored local HTTP profile path")
	flags.StringVar(&settings.StateDir, "state-dir", settings.StateDir, "local database and secret directory")
	flags.Var(&roots, "catalog-root", "workspace-relative catalog file or directory; repeatable")
	flags.Var(&candidates, "candidate-root", "workspace-relative executable candidate directory; repeatable")
	flags.Var(&ruleFiles, "rule-file", "rule filename discovered from workspace root to target; repeatable")
	flags.Var(&symbolIndexes, "symbol-index", "workspace-relative symbol-index.v1 or ctags JSONL; repeatable")
	flags.BoolVar(&settings.EnableIntentionWrites, "enable-intention-writes", false, "pre-authorize checkpoint and memo writes without runtime prompting")
	flags.BoolVar(&settings.EnableReportImport, "enable-report-import", false, "pre-authorize local check-report import without runtime prompting")
	flags.BoolVar(&settings.EnableRunner, "enable-runner", false, "pre-authorize registered Runner use without runtime prompting")
	flags.BoolVar(&settings.EnableCache, "enable-cache", false, "pre-authorize qualified Runner cache reuse; requires Runner")
	if err := flags.Parse(args); err != nil {
		return Settings{}, err
	}
	if flags.NArg() != 0 {
		return Settings{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if settings.EnableCache && !settings.EnableRunner {
		return Settings{}, fmt.Errorf("--enable-cache requires --enable-runner")
	}
	if settings.Transport != "stdio" && settings.Transport != "http" {
		return Settings{}, fmt.Errorf("--transport must be stdio or http")
	}
	if settings.Transport == "http" && strings.TrimSpace(settings.HTTPProfile) == "" {
		return Settings{}, fmt.Errorf("--http-profile is required for HTTP transport")
	}
	if settings.Transport == "stdio" && settings.HTTPProfile != "" {
		return Settings{}, fmt.Errorf("--http-profile requires --transport=http")
	}
	if len(roots) == 0 {
		roots = append(roots, defaultCatalogRoot(settings.Workspace, workingDirectory))
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
	settings.SymbolIndexes = append([]string(nil), symbolIndexes...)
	return settings, nil
}

func defaultCatalogRoot(workspacePath, startPath string) string {
	const fallback = "catalog"
	workspaceAbsolute, err := filepath.Abs(workspacePath)
	if err != nil {
		return fallback
	}
	if info, statErr := os.Stat(filepath.Join(workspaceAbsolute, fallback)); statErr == nil && info.IsDir() {
		return fallback
	}
	current, err := filepath.Abs(startPath)
	if err != nil || !pathWithin(workspaceAbsolute, current) {
		return fallback
	}
	for pathWithin(workspaceAbsolute, current) {
		if _, statErr := os.Stat(filepath.Join(current, ".git")); statErr == nil {
			catalogPath := filepath.Join(current, fallback)
			if info, catalogErr := os.Stat(catalogPath); catalogErr == nil && info.IsDir() {
				relative, relErr := filepath.Rel(workspaceAbsolute, catalogPath)
				if relErr == nil && pathWithin(workspaceAbsolute, catalogPath) {
					return filepath.ToSlash(relative)
				}
			}
			return fallback
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	if nested := uniqueChildCatalogRoot(workspaceAbsolute); nested != "" {
		return nested
	}
	return fallback
}

func uniqueChildCatalogRoot(workspaceAbsolute string) string {
	entries, err := os.ReadDir(workspaceAbsolute)
	if err != nil || len(entries) > 64 {
		return ""
	}
	candidate := ""
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		repository := filepath.Join(workspaceAbsolute, entry.Name())
		if _, err := os.Stat(filepath.Join(repository, ".git")); err != nil {
			continue
		}
		catalogPath := filepath.Join(repository, "catalog")
		if info, err := os.Stat(catalogPath); err != nil || !info.IsDir() {
			continue
		}
		if candidate != "" {
			return ""
		}
		candidate = filepath.ToSlash(filepath.Join(entry.Name(), "catalog"))
	}
	return candidate
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
