// Package search implements bounded workspace filename and text search.
package search

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"repoplane/internal/content"
	"repoplane/internal/textcodec"
)

const (
	MaxMatches         = 10_000
	MaxRGRecordBytes   = 1024 * 1024
	MaxStderrBytes     = 8 * 1024
	MaxSearchFileBytes = 16 * 1024 * 1024
)

var ErrBackendUnavailable = errors.New("search: ripgrep backend unavailable")

type BackendQuery struct {
	Mode          string
	Pattern       string
	Directory     string
	Includes      []string
	Excludes      []string
	Hidden        bool
	Ignored       string
	Generated     string
	Vendor        string
	CaseSensitive bool
	Encoding      string
	MatchKind     string
	Revision      string
	Since         string
	Until         string
	SymbolKind    string
	Language      string
	PatternSyntax string
	RootPrefix    string
}

type Match struct {
	Path      string
	Line      uint64
	Text      string
	SourceRef string
	Basis     string
	Channel   string
	Revision  string
	Validity  string
}

type BackendOutcome struct {
	Matches     []Match
	Complete    bool
	Warnings    []string
	Engine      string
	Validity    string
	Unsupported bool
}

type Backend interface {
	Search(context.Context, BackendQuery) (BackendOutcome, error)
}

// UnavailableBackend keeps searches explicit when ripgrep cannot be started.
// Git history and configured symbol search can still run through AdapterBackend.
type UnavailableBackend struct{}

func (UnavailableBackend) Search(context.Context, BackendQuery) (BackendOutcome, error) {
	return BackendOutcome{
		Matches: []Match{}, Unsupported: true, Engine: "ripgrep unavailable",
		Warnings: []string{"ripgrep (rg) is unavailable; install rg or use a release archive that includes it to enable filename and text search"},
	}, nil
}

type RGBackend struct {
	executable string
	version    string
}

func NewRGBackend(ctx context.Context) (*RGBackend, error) {
	path, err := rgExecutable()
	if err != nil {
		return nil, ErrBackendUnavailable
	}
	command := exec.CommandContext(ctx, path, "--version")
	var output cappedBuffer
	output.max = 1024
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%w: version probe: %v", ErrBackendUnavailable, err)
	}
	version := strings.TrimSpace(strings.SplitN(output.String(), "\n", 2)[0])
	return &RGBackend{executable: path, version: version}, nil
}

func rgExecutable() (string, error) {
	executable, err := os.Executable()
	if err == nil {
		return rgExecutableNear(executable)
	}
	return exec.LookPath("rg")
}

func rgExecutableNear(executable string) (string, error) {
	name := "rg"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	adjacent := filepath.Join(filepath.Dir(executable), name)
	if info, err := os.Stat(adjacent); err == nil && info.Mode().IsRegular() {
		return adjacent, nil
	}
	return exec.LookPath("rg")
}

func (b *RGBackend) Search(ctx context.Context, query BackendQuery) (BackendOutcome, error) {
	if query.Encoding == "" {
		query.Encoding = "utf-8"
	}
	switch query.Mode {
	case "filename":
		return b.searchFiles(ctx, query)
	case "exact", "regex":
		if query.Encoding == "cp949" || query.Encoding == "euc-kr" {
			return b.searchDecoded(ctx, query)
		}
		return b.searchText(ctx, query)
	default:
		return BackendOutcome{}, fmt.Errorf("unsupported search mode %q", query.Mode)
	}
}

func (b *RGBackend) searchDecoded(ctx context.Context, query BackendQuery) (BackendOutcome, error) {
	files, err := b.searchFiles(ctx, BackendQuery{
		Mode: "filename", Directory: query.Directory, Includes: query.Includes, Excludes: query.Excludes,
		Hidden: query.Hidden, Ignored: query.Ignored, Generated: query.Generated, Vendor: query.Vendor,
		CaseSensitive: true, Encoding: query.Encoding,
	})
	if err != nil {
		return BackendOutcome{}, err
	}
	outcome := BackendOutcome{
		Matches: make([]Match, 0), Complete: files.Complete,
		Warnings: append([]string(nil), files.Warnings...),
		Engine:   b.version + " file enumeration + Go textcodec/regexp",
	}
	var expression *regexp.Regexp
	if query.Mode == "regex" {
		pattern := query.Pattern
		if !query.CaseSensitive {
			pattern = "(?i:" + pattern + ")"
		}
		expression, err = regexp.Compile(pattern)
		if err != nil {
			return BackendOutcome{}, fmt.Errorf("invalid regular expression: %w", err)
		}
	}
	for _, candidate := range files.Matches {
		if err := ctx.Err(); err != nil {
			outcome.Complete = false
			outcome.Warnings = append(outcome.Warnings, "search deadline or cancellation reached")
			break
		}
		path := filepath.Join(query.Directory, filepath.FromSlash(candidate.Path))
		file, err := os.Open(path)
		if err != nil {
			outcome.Complete = false
			outcome.Warnings = append(outcome.Warnings, "could not read "+candidate.Path)
			continue
		}
		data, truncated, readErr := content.ReadBounded(file, MaxSearchFileBytes)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			outcome.Complete = false
			outcome.Warnings = append(outcome.Warnings, "could not read "+candidate.Path)
			continue
		}
		if truncated {
			outcome.Complete = false
			outcome.Warnings = append(outcome.Warnings, "file exceeds search size limit: "+candidate.Path)
			continue
		}
		text, err := textcodec.Decode(query.Encoding, data)
		if err != nil {
			outcome.Complete = false
			outcome.Warnings = append(outcome.Warnings, "decode failed for "+candidate.Path)
			continue
		}
		for lineIndex, line := range strings.Split(text, "\n") {
			line = strings.TrimSuffix(line, "\r")
			matched := false
			if expression != nil {
				matched = expression.MatchString(line)
			} else if query.CaseSensitive {
				matched = strings.Contains(line, query.Pattern)
			} else {
				matched = strings.Contains(strings.ToLower(line), strings.ToLower(query.Pattern))
			}
			if !matched {
				continue
			}
			outcome.Matches = append(outcome.Matches, Match{Path: candidate.Path, Line: uint64(lineIndex + 1), Text: line})
			if len(outcome.Matches) >= MaxMatches {
				outcome.Complete = false
				outcome.Warnings = append(outcome.Warnings, fmt.Sprintf("match limit %d reached", MaxMatches))
				return outcome, nil
			}
		}
	}
	return outcome, nil
}

func (b *RGBackend) baseArguments(query BackendQuery) []string {
	arguments := []string{"--no-config", "--color", "never", "--sort", "path"}
	if query.Hidden {
		arguments = append(arguments, "--hidden")
	}
	if query.Ignored == "include" {
		arguments = append(arguments, "--no-ignore")
	}
	for _, include := range query.Includes {
		arguments = append(arguments, "--glob", include)
	}
	for _, exclude := range query.Excludes {
		arguments = append(arguments, "--glob", "!"+exclude)
	}
	if query.Generated != "include" {
		for _, pattern := range []string{"!generated/**", "!dist/**", "!build/**"} {
			arguments = append(arguments, "--glob", pattern)
		}
	}
	if query.Vendor != "include" {
		for _, pattern := range []string{"!vendor/**", "!node_modules/**"} {
			arguments = append(arguments, "--glob", pattern)
		}
	}
	return arguments
}

func (b *RGBackend) searchFiles(ctx context.Context, query BackendQuery) (BackendOutcome, error) {
	arguments := append(b.baseArguments(query), "--files", "--null", ".")
	matcher, err := filenameMatcher(query.Pattern, query.CaseSensitive)
	if err != nil {
		return BackendOutcome{}, err
	}
	return b.run(ctx, query.Directory, arguments, bufio.ScanWords, func(token []byte) (*Match, error) {
		path := filepath.ToSlash(string(token))
		path = strings.TrimPrefix(path, "./")
		if !matcher(path) {
			return nil, nil
		}
		return &Match{Path: path}, nil
	}, scanNull)
}

func (b *RGBackend) searchText(ctx context.Context, query BackendQuery) (BackendOutcome, error) {
	// --text prevents ripgrep's binary heuristic from silently omitting a file.
	// Invalid UTF-8 is still surfaced by the JSON parser as an explicit failure.
	arguments := append(b.baseArguments(query), "--json", "--line-number", "--with-filename", "--text", "--encoding", "utf-8")
	if query.Mode == "exact" {
		arguments = append(arguments, "--fixed-strings")
	}
	if !query.CaseSensitive {
		arguments = append(arguments, "--ignore-case")
	}
	arguments = append(arguments, "--", query.Pattern, ".")
	return b.run(ctx, query.Directory, arguments, bufio.ScanLines, parseRGMatch, nil)
}

type rgEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text *string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text *string `json:"text"`
		} `json:"lines"`
		LineNumber *uint64 `json:"line_number"`
	} `json:"data"`
}

func parseRGMatch(record []byte) (*Match, error) {
	var event rgEvent
	if err := json.Unmarshal(record, &event); err != nil {
		return nil, fmt.Errorf("decode ripgrep event: %w", err)
	}
	if event.Type != "match" {
		return nil, nil
	}
	if event.Data.Path.Text == nil || event.Data.Lines.Text == nil || event.Data.LineNumber == nil {
		return nil, errors.New("ripgrep returned a non-text path or line")
	}
	path := strings.TrimPrefix(filepath.ToSlash(*event.Data.Path.Text), "./")
	return &Match{Path: path, Line: *event.Data.LineNumber, Text: strings.TrimSuffix(*event.Data.Lines.Text, "\n")}, nil
}

func (b *RGBackend) run(
	ctx context.Context,
	directory string,
	arguments []string,
	defaultSplit bufio.SplitFunc,
	parse func([]byte) (*Match, error),
	overrideSplit bufio.SplitFunc,
) (BackendOutcome, error) {
	childContext, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(childContext, b.executable, arguments...)
	command.Dir = directory
	stdout, err := command.StdoutPipe()
	if err != nil {
		return BackendOutcome{}, err
	}
	var stderr cappedBuffer
	stderr.max = MaxStderrBytes
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return BackendOutcome{}, fmt.Errorf("start ripgrep: %w", err)
	}

	outcome := BackendOutcome{Matches: make([]Match, 0), Complete: true, Engine: b.version}
	scanner := bufio.NewScanner(stdout)
	split := defaultSplit
	if overrideSplit != nil {
		split = overrideSplit
	}
	scanner.Split(split)
	scanner.Buffer(make([]byte, 64*1024), MaxRGRecordBytes)
	for scanner.Scan() {
		match, parseErr := parse(scanner.Bytes())
		if parseErr != nil {
			cancel()
			_ = command.Wait()
			return BackendOutcome{}, parseErr
		}
		if match == nil {
			continue
		}
		outcome.Matches = append(outcome.Matches, *match)
		if len(outcome.Matches) >= MaxMatches {
			outcome.Complete = false
			outcome.Warnings = append(outcome.Warnings, fmt.Sprintf("match limit %d reached", MaxMatches))
			cancel()
			break
		}
	}
	scanErr := scanner.Err()
	waitErr := command.Wait()
	if scanErr != nil && ctx.Err() == nil && outcome.Complete {
		return BackendOutcome{}, fmt.Errorf("read ripgrep output: %w", scanErr)
	}
	if ctx.Err() != nil {
		outcome.Complete = false
		outcome.Warnings = append(outcome.Warnings, "search deadline or cancellation reached")
	} else if waitErr != nil && outcome.Complete {
		var exitError *exec.ExitError
		if !errors.As(waitErr, &exitError) || exitError.ExitCode() != 1 {
			if len(outcome.Matches) == 0 {
				return BackendOutcome{}, fmt.Errorf("ripgrep failed: %s", strings.TrimSpace(stderr.String()))
			}
			outcome.Complete = false
			outcome.Warnings = append(outcome.Warnings, "ripgrep reported an error after returning matches")
		}
	}
	if stderr.Len() > 0 {
		outcome.Complete = false
		message := "ripgrep reported diagnostics"
		if stderr.truncated {
			message += "; diagnostics exceeded the capture limit"
		}
		outcome.Warnings = append(outcome.Warnings, message)
	}
	return outcome, nil
}

func filenameMatcher(pattern string, caseSensitive bool) (func(string) bool, error) {
	if pattern == "" {
		return func(string) bool { return true }, nil
	}
	if !caseSensitive {
		pattern = strings.ToLower(pattern)
	}
	return func(path string) bool {
		if !caseSensitive {
			path = strings.ToLower(path)
		}
		return strings.Contains(path, pattern)
	}, nil
}

func scanNull(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if index := bytes.IndexByte(data, 0); index >= 0 {
		return index + 1, data[:index], nil
	}
	if atEOF && len(data) != 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

type cappedBuffer struct {
	bytes.Buffer
	max       int
	truncated bool
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.max - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(data) > remaining {
		_, _ = b.Buffer.Write(data[:remaining])
		b.truncated = true
		return original, nil
	}
	_, _ = b.Buffer.Write(data)
	return original, nil
}

var _ io.Writer = (*cappedBuffer)(nil)
