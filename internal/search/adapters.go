package search

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const maxAdapterOutput = 16 * 1024 * 1024

// AdapterBackend keeps optional search channels behind the existing Backend
// contract so their absence cannot disable ripgrep search.
type AdapterBackend struct {
	base          Backend
	workspaceRoot string
	git           string
	gitVersion    string
	symbolIndexes []string
}

func NewAdapterBackend(ctx context.Context, base Backend, workspaceRoot string, symbolIndexes []string) *AdapterBackend {
	git, _ := exec.LookPath("git")
	version := ""
	if git != "" {
		command := exec.CommandContext(ctx, git, "--version")
		var output cappedBuffer
		output.max = 1024
		command.Stdout = &output
		command.Stderr = &output
		if command.Run() == nil {
			version = strings.TrimSpace(output.String())
		}
	}
	return &AdapterBackend{base: base, workspaceRoot: workspaceRoot, git: git, gitVersion: version, symbolIndexes: append([]string(nil), symbolIndexes...)}
}

func (b *AdapterBackend) Search(ctx context.Context, query BackendQuery) (BackendOutcome, error) {
	if relative, err := filepath.Rel(b.workspaceRoot, query.Directory); err == nil && relative != "." {
		query.RootPrefix = strings.TrimSuffix(filepath.ToSlash(relative), "/") + "/"
	}
	switch query.Mode {
	case "git_history":
		return b.searchGit(ctx, query)
	case "symbol":
		return b.searchSymbols(ctx, query)
	default:
		return b.base.Search(ctx, query)
	}
}

func (b *AdapterBackend) searchGit(ctx context.Context, query BackendQuery) (BackendOutcome, error) {
	if b.git == "" {
		return BackendOutcome{Matches: []Match{}, Unsupported: true, Engine: "git unavailable", Warnings: []string{"Git history adapter is unavailable"}}, nil
	}
	revision := query.Revision
	if revision == "" {
		revision = "HEAD"
	}
	resolved, err := b.gitText(ctx, query.Directory, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return BackendOutcome{}, fmt.Errorf("resolve Git revision: %w", err)
	}
	commit := strings.TrimSpace(resolved)
	if !regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`).MatchString(commit) {
		return BackendOutcome{}, errors.New("Git returned an invalid object ID")
	}
	shallowText, _ := b.gitText(ctx, query.Directory, "rev-parse", "--is-shallow-repository")
	shallow := strings.TrimSpace(shallowText) == "true"
	kind := query.MatchKind
	if kind == "" {
		kind = "all"
	}
	engine := b.gitVersion
	if engine == "" {
		engine = "git"
	}
	outcome := BackendOutcome{Matches: make([]Match, 0), Complete: true, Engine: engine, Validity: "current"}
	if shallow {
		outcome.Complete = false
		outcome.Warnings = append(outcome.Warnings, "Git repository is shallow; older history is unknown")
	}
	if kind == "all" || kind == "commit" {
		matches, complete, warnings, err := b.gitCommitMatches(ctx, query, commit)
		if err != nil {
			return BackendOutcome{}, err
		}
		outcome.Matches = append(outcome.Matches, matches...)
		outcome.Complete = outcome.Complete && complete
		outcome.Warnings = append(outcome.Warnings, warnings...)
	}
	if kind == "all" || kind == "path" {
		matches, complete, warnings, err := b.gitPathMatches(ctx, query, commit)
		if err != nil {
			return BackendOutcome{}, err
		}
		outcome.Matches = append(outcome.Matches, matches...)
		outcome.Complete = outcome.Complete && complete
		outcome.Warnings = append(outcome.Warnings, warnings...)
	}
	if kind == "all" || kind == "diff" {
		matches, complete, warnings, err := b.gitDiffMatches(ctx, query, commit)
		if err != nil {
			return BackendOutcome{}, err
		}
		outcome.Matches = append(outcome.Matches, matches...)
		outcome.Complete = outcome.Complete && complete
		outcome.Warnings = append(outcome.Warnings, warnings...)
	}
	if len(outcome.Matches) > MaxMatches {
		outcome.Matches = outcome.Matches[:MaxMatches]
		outcome.Complete = false
		outcome.Warnings = append(outcome.Warnings, fmt.Sprintf("match limit %d reached", MaxMatches))
	}
	return outcome, nil
}

func (b *AdapterBackend) gitBaseArgs(query BackendQuery) []string {
	args := []string{"--no-pager", "-c", "color.ui=false", "log", "--no-ext-diff", "--no-textconv", "--no-renames", "--format=%H%x00%s%x00", "-z"}
	if query.Since != "" {
		args = append(args, "--since="+query.Since)
	}
	if query.Until != "" {
		args = append(args, "--until="+query.Until)
	}
	return args
}

func (b *AdapterBackend) gitCommitMatches(ctx context.Context, query BackendQuery, commit string) ([]Match, bool, []string, error) {
	args := b.gitBaseArgs(query)
	args = append(args, "--grep="+query.Pattern)
	if query.PatternSyntax == "regex" {
		args = append(args, "--extended-regexp")
	} else {
		args = append(args, "--fixed-strings")
	}
	if !query.CaseSensitive {
		args = append(args, "--regexp-ignore-case")
	}
	args = append(args, commit, "--", ".")
	data, complete, warning, err := b.runGit(ctx, query.Directory, args...)
	if err != nil {
		return nil, false, nil, err
	}
	return parseGitPairs(data, "commit"), complete, warningList(warning), nil
}

func (b *AdapterBackend) gitDiffMatches(ctx context.Context, query BackendQuery, commit string) ([]Match, bool, []string, error) {
	args := b.gitBaseArgs(query)
	selector := "-S" + query.Pattern
	if query.PatternSyntax == "regex" {
		selector = "-G" + query.Pattern
	}
	args = append(args, selector)
	if !query.CaseSensitive {
		args = append(args, "--regexp-ignore-case")
	}
	args = append(args, commit, "--", ".")
	data, complete, warning, err := b.runGit(ctx, query.Directory, args...)
	if err != nil {
		return nil, false, nil, err
	}
	return parseGitPairs(data, "diff"), complete, warningList(warning), nil
}

func (b *AdapterBackend) gitPathMatches(ctx context.Context, query BackendQuery, commit string) ([]Match, bool, []string, error) {
	args := b.gitBaseArgs(query)
	args = append(args, "--name-only", commit, "--", ".")
	data, complete, warning, err := b.runGit(ctx, query.Directory, args...)
	if err != nil {
		return nil, false, nil, err
	}
	tokens := bytes.Split(data, []byte{0})
	matches := make([]Match, 0)
	var revision string
	for index := 0; index < len(tokens); index++ {
		token := strings.TrimSpace(string(tokens[index]))
		if token == "" {
			continue
		}
		if regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`).MatchString(token) && index+1 < len(tokens) {
			revision = token
			index++ // subject
			continue
		}
		path := filepath.ToSlash(token)
		if query.RootPrefix != "" {
			if !strings.HasPrefix(path, query.RootPrefix) {
				continue
			}
			path = strings.TrimPrefix(path, query.RootPrefix)
		}
		if revision == "" || !matchText(path, query.Pattern, query.CaseSensitive, query.PatternSyntax == "regex") || !pathInScope(path, query) {
			continue
		}
		matches = append(matches, gitMatch(revision, path, path, "path"))
	}
	return matches, complete, warningList(warning), nil
}

func parseGitPairs(data []byte, channel string) []Match {
	tokens := bytes.Split(data, []byte{0})
	matches := make([]Match, 0, len(tokens)/2)
	for index := 0; index+1 < len(tokens); index += 2 {
		revision := strings.TrimSpace(string(tokens[index]))
		text := strings.TrimSpace(string(tokens[index+1]))
		if revision == "" || !regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`).MatchString(revision) {
			continue
		}
		matches = append(matches, gitMatch(revision, "", text, channel))
	}
	return matches
}

func gitMatch(revision, path, text, channel string) Match {
	return Match{Path: path, Text: text, SourceRef: "git:" + revision, Basis: "git_revision", Channel: channel, Revision: revision, Validity: "current"}
}

func (b *AdapterBackend) runGit(ctx context.Context, directory string, args ...string) ([]byte, bool, string, error) {
	command := exec.CommandContext(ctx, b.git, args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr cappedBuffer
	stdout.max, stderr.max = maxAdapterOutput, MaxStderrBytes
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() != nil {
		return stdout.Bytes(), false, "Git history deadline or cancellation reached", nil
	}
	if err != nil {
		return nil, false, "", fmt.Errorf("Git history query failed: %s", strings.TrimSpace(stderr.String()))
	}
	complete := !stdout.truncated
	warning := ""
	if stdout.truncated {
		warning = "Git history output limit reached"
	} else if stderr.Len() != 0 {
		complete, warning = false, "Git history reported diagnostics"
	}
	return append([]byte(nil), stdout.Bytes()...), complete, warning, nil
}

func (b *AdapterBackend) gitText(ctx context.Context, directory string, args ...string) (string, error) {
	data, _, _, err := b.runGit(ctx, directory, args...)
	return string(data), err
}

type symbolRecord struct {
	Type           string `json:"_type"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Language       string `json:"language"`
	Path           string `json:"path"`
	Line           any    `json:"line"`
	SourceRevision string `json:"source_revision"`
}

func (b *AdapterBackend) searchSymbols(ctx context.Context, query BackendQuery) (BackendOutcome, error) {
	if len(b.symbolIndexes) == 0 {
		return BackendOutcome{Matches: []Match{}, Unsupported: true, Engine: "symbol index unavailable", Warnings: []string{"no symbol index is configured"}}, nil
	}
	head := ""
	worktreeClean := false
	if b.git != "" {
		head, _ = b.gitText(ctx, b.workspaceRoot, "rev-parse", "--verify", "HEAD^{commit}")
		head = strings.TrimSpace(head)
		status, complete, _, err := b.runGit(ctx, b.workspaceRoot, "status", "--porcelain=v1", "-z", "--untracked-files=all")
		worktreeClean = err == nil && complete && len(status) == 0
	}
	outcome := BackendOutcome{Matches: make([]Match, 0), Complete: true, Engine: "symbol-index.v1/ctags-json", Validity: "current"}
	readIndexes := 0
	for _, configured := range b.symbolIndexes {
		absolute := configured
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(b.workspaceRoot, filepath.FromSlash(configured))
		}
		relative, err := filepath.Rel(b.workspaceRoot, absolute)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return BackendOutcome{}, errors.New("symbol index resolves outside workspace")
		}
		file, err := os.Open(absolute)
		if errors.Is(err, os.ErrNotExist) {
			outcome.Complete, outcome.Validity = false, "unknown"
			outcome.Warnings = append(outcome.Warnings, "configured symbol index is missing")
			continue
		}
		if err != nil {
			return BackendOutcome{}, errors.New("configured symbol index cannot be read")
		}
		readIndexes++
		resolvedIndex, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			_ = file.Close()
			return BackendOutcome{}, errors.New("configured symbol index cannot be resolved")
		}
		resolvedRelative, err := filepath.Rel(b.workspaceRoot, resolvedIndex)
		if err != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
			_ = file.Close()
			return BackendOutcome{}, errors.New("symbol index resolves outside workspace")
		}
		hasher := sha256.New()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), MaxRGRecordBytes)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			_, _ = hasher.Write(scanner.Bytes())
			_, _ = hasher.Write([]byte{'\n'})
			if err := ctx.Err(); err != nil {
				outcome.Complete = false
				outcome.Warnings = append(outcome.Warnings, "symbol scan deadline or cancellation reached")
				break
			}
			var record symbolRecord
			decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
			decoder.UseNumber()
			if err := decoder.Decode(&record); err != nil {
				outcome.Complete = false
				outcome.Warnings = append(outcome.Warnings, fmt.Sprintf("malformed symbol record at line %d", lineNumber))
				continue
			}
			if record.Type != "" && record.Type != "tag" {
				continue
			}
			if record.Name == "" || !safeSymbolPath(record.Path) || (query.SymbolKind != "" && record.Kind != query.SymbolKind) || (query.Language != "" && !strings.EqualFold(record.Language, query.Language)) {
				continue
			}
			channel := symbolChannel(record.Name, query.Pattern, query.CaseSensitive)
			resultPath := filepath.ToSlash(record.Path)
			if query.RootPrefix != "" {
				if !strings.HasPrefix(resultPath, query.RootPrefix) {
					continue
				}
				resultPath = strings.TrimPrefix(resultPath, query.RootPrefix)
			}
			if channel == "" || !pathInScope(resultPath, query) {
				continue
			}
			validity := "unknown"
			if record.SourceRevision != "" && head != "" {
				if record.SourceRevision != head {
					validity = "stale"
				} else if worktreeClean {
					validity = "current"
				}
			}
			line := symbolLine(record.Line)
			outcome.Matches = append(outcome.Matches, Match{Path: resultPath, Line: line, Text: record.Name, SourceRef: "source:mutable:" + filepath.ToSlash(record.Path), Basis: "generated_index", Channel: channel, Revision: record.SourceRevision, Validity: validity})
			if validity != "current" {
				outcome.Validity = validity
			}
			if len(outcome.Matches) >= MaxMatches {
				outcome.Complete = false
				break
			}
		}
		_ = file.Close()
		if err := scanner.Err(); err != nil {
			outcome.Complete = false
			outcome.Warnings = append(outcome.Warnings, "symbol index record exceeded the supported limit")
		}
		_ = hex.EncodeToString(hasher.Sum(nil)) // consume bounded index identity without exposing its path
	}
	if readIndexes == 0 {
		outcome.Unsupported = true
		outcome.Complete = false
	}
	if len(outcome.Matches) == 0 && len(outcome.Warnings) > 0 {
		outcome.Validity = "unknown"
	}
	sort.SliceStable(outcome.Matches, func(i, j int) bool {
		weight := map[string]int{"exact": 0, "prefix": 1, "substring": 2}
		if weight[outcome.Matches[i].Channel] != weight[outcome.Matches[j].Channel] {
			return weight[outcome.Matches[i].Channel] < weight[outcome.Matches[j].Channel]
		}
		if outcome.Matches[i].Text != outcome.Matches[j].Text {
			return outcome.Matches[i].Text < outcome.Matches[j].Text
		}
		return outcome.Matches[i].Path < outcome.Matches[j].Path
	})
	return outcome, nil
}

func safeSymbolPath(path string) bool {
	clean := filepath.Clean(filepath.FromSlash(path))
	return path != "" && !filepath.IsAbs(clean) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func symbolLine(value any) uint64 {
	switch typed := value.(type) {
	case json.Number:
		result, _ := strconv.ParseUint(typed.String(), 10, 64)
		return result
	case float64:
		return uint64(typed)
	case string:
		result, _ := strconv.ParseUint(typed, 10, 64)
		return result
	default:
		return 0
	}
}

func symbolChannel(name, pattern string, caseSensitive bool) string {
	left, right := name, pattern
	if !caseSensitive {
		left, right = strings.ToLower(left), strings.ToLower(right)
	}
	if left == right {
		return "exact"
	}
	if strings.HasPrefix(left, right) {
		return "prefix"
	}
	if strings.Contains(left, right) {
		return "substring"
	}
	return ""
}

func matchText(value, pattern string, caseSensitive, regexMode bool) bool {
	if regexMode {
		if !caseSensitive {
			pattern = "(?i:" + pattern + ")"
		}
		expression, err := regexp.Compile(pattern)
		return err == nil && expression.MatchString(value)
	}
	if !caseSensitive {
		value, pattern = strings.ToLower(value), strings.ToLower(pattern)
	}
	return strings.Contains(value, pattern)
}

func pathInScope(path string, query BackendQuery) bool {
	path = filepath.ToSlash(strings.TrimPrefix(path, "./"))
	for _, exclude := range query.Excludes {
		if matched, _ := filepath.Match(exclude, path); matched {
			return false
		}
	}
	if query.Generated != "include" && (strings.HasPrefix(path, "generated/") || strings.HasPrefix(path, "dist/") || strings.HasPrefix(path, "build/")) {
		return false
	}
	if query.Vendor != "include" && (strings.HasPrefix(path, "vendor/") || strings.HasPrefix(path, "node_modules/")) {
		return false
	}
	if len(query.Includes) == 0 {
		return true
	}
	for _, include := range query.Includes {
		if matched, _ := filepath.Match(include, path); matched {
			return true
		}
	}
	return false
}

func warningList(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

var _ Backend = (*AdapterBackend)(nil)
