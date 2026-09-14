// Package pathfacts explains observable facts about a workspace path.
package pathfacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"repoplane/internal/content"
	"repoplane/internal/contracts"
	"repoplane/internal/search"
	"repoplane/internal/textcodec"
	"repoplane/internal/workspace"
)

const MaxInspectBytes uint64 = 1024 * 1024

var ErrResponseTooLarge = errors.New("path facts: response cannot fit byte_limit")

type Request struct {
	Path        string `json:"path" jsonschema:"workspace-relative path to explain"`
	Action      string `json:"action,omitempty" jsonschema:"intended action such as read or edit"`
	Encoding    string `json:"encoding,omitempty" jsonschema:"explicit codec to test: utf-8, cp949, or euc-kr"`
	ByteLimit   uint64 `json:"byte_limit,omitempty" jsonschema:"response byte limit"`
	TimeLimitMS int64  `json:"time_limit_ms,omitempty" jsonschema:"deadline ms"`
}

type Newlines struct {
	CRLF         uint64 `json:"crlf"`
	LF           uint64 `json:"lf"`
	CR           uint64 `json:"cr"`
	FinalNewline *bool  `json:"final_newline"`
	Partial      bool   `json:"partial"`
}

type EncodingFacts struct {
	BOM                string   `json:"bom"`
	UTF8Valid          *bool    `json:"utf8_valid"`
	Candidates         []string `json:"candidates"`
	Requested          string   `json:"requested,omitempty"`
	DecodeSuccess      *bool    `json:"decode_success"`
	ByteRoundTrip      *bool    `json:"byte_round_trip"`
	ObservationPartial bool     `json:"observation_partial"`
}

type GitFacts struct {
	State          string `json:"state"`
	RepositoryRoot string `json:"repository_root,omitempty"`
	WorktreeID     string `json:"worktree_id,omitempty"`
	Tracked        *bool  `json:"tracked"`
	Ignored        *bool  `json:"ignored"`
	Untracked      *bool  `json:"untracked"`
}

type Rule struct {
	Path      string `json:"path"`
	ScopeRoot string `json:"scope_root"`
	SourceRef string `json:"source_ref"`
}

type Facts struct {
	WorkspaceID        string        `json:"workspace_id"`
	Path               string        `json:"path"`
	ResolvedPath       string        `json:"resolved_path"`
	Exists             bool          `json:"exists"`
	Kind               string        `json:"kind"`
	ResolvedKind       string        `json:"resolved_kind"`
	Size               *int64        `json:"size"`
	ContentHash        string        `json:"content_hash,omitempty"`
	Links              LinkFacts     `json:"links"`
	Git                GitFacts      `json:"git"`
	Encoding           EncodingFacts `json:"encoding"`
	Newlines           Newlines      `json:"newlines"`
	Rules              []Rule        `json:"rules"`
	BasenameCandidates []string      `json:"basename_candidates"`
	DeclaredRoles      []string      `json:"declared_roles"`
	Action             string        `json:"action,omitempty"`
}

type Response = contracts.Response[Facts]

type Service struct {
	root      *workspace.Root
	search    search.Backend
	ruleFiles []string
}

func NewService(root *workspace.Root, backend search.Backend, ruleFiles []string) *Service {
	if len(ruleFiles) == 0 {
		ruleFiles = []string{"AGENTS.md"}
	}
	return &Service{root: root, search: backend, ruleFiles: append([]string(nil), ruleFiles...)}
}

func (s *Service) Explain(ctx context.Context, request Request) (Response, error) {
	if request.Path == "" {
		return Response{}, errors.New("path is required")
	}
	limits, err := contracts.NormalizeLimits(contracts.LimitRequest{ByteLimit: request.ByteLimit, TimeLimitMS: request.TimeLimitMS})
	if err != nil {
		return Response{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, limits.TimeLimit)
	defer cancel()
	resolved, exists, err := s.root.ResolveForLookup(filepath.FromSlash(request.Path))
	if err != nil {
		return Response{}, err
	}
	relative, err := filepath.Rel(s.root.Resolved(), resolved)
	if err != nil {
		return Response{}, err
	}
	relative = filepath.ToSlash(relative)
	facts := Facts{
		WorkspaceID: s.root.ID(), Path: filepath.ToSlash(filepath.Clean(request.Path)), ResolvedPath: relative,
		Exists: exists, Kind: "missing", Rules: make([]Rule, 0), BasenameCandidates: make([]string, 0),
		DeclaredRoles: make([]string, 0), Action: request.Action,
		Encoding: EncodingFacts{BOM: "none", Candidates: make([]string, 0)},
	}
	partial := false
	warnings := make([]contracts.Warning, 0)
	if exists {
		lexical := filepath.Join(s.root.Lexical(), filepath.Clean(filepath.FromSlash(request.Path)))
		requestedInfo, err := os.Lstat(lexical)
		if err != nil {
			return Response{}, err
		}
		resolvedInfo, err := os.Stat(resolved)
		if err != nil {
			return Response{}, err
		}
		facts.Kind = fileKind(requestedInfo)
		facts.ResolvedKind = fileKind(resolvedInfo)
		size := resolvedInfo.Size()
		facts.Size = &size
		facts.Links = observeLinks(lexical, requestedInfo)
		if resolvedInfo.Mode().IsRegular() {
			if err := inspectFile(resolved, request.Encoding, &facts); err != nil {
				warnings = append(warnings, contracts.Warning{Code: "inspection_failed", Message: "file inspection failed"})
				partial = true
			}
			if facts.Encoding.ObservationPartial {
				partial = true
				warnings = append(warnings, contracts.Warning{Code: "inspection_truncated", Message: "file exceeds inspection byte limit"})
			}
		}
	}
	facts.Git = inspectGit(ctx, s.root, resolved, exists)
	facts.Rules = s.rulesFor(resolved, exists)
	duplicates, complete, duplicateWarnings := s.basenameCandidates(ctx, filepath.Base(resolved))
	facts.BasenameCandidates = duplicates
	if !complete {
		partial = true
	}
	for _, message := range duplicateWarnings {
		warnings = append(warnings, contracts.Warning{Code: "basename_scan", Message: message})
	}

	status := contracts.StatusOK
	scanState := contracts.ScanComplete
	if partial {
		status = contracts.StatusPartial
		scanState = contracts.ScanPartial
	}
	one := uint64(1)
	truncated := false
	scope := "scope:path:" + s.root.ID()
	response := Response{
		Status: status, Items: []Facts{facts},
		Counts: contracts.Counts{Matched: &one, Relation: contracts.CountExact, Returned: 1},
		Scan:   contracts.Scan{State: scanState, ScopeRef: &scope}, Truncated: &truncated,
		Warnings: warnings,
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return Response{}, err
	}
	if uint64(len(encoded)) > limits.ByteLimit {
		return Response{}, ErrResponseTooLarge
	}
	return response, nil
}

func inspectFile(path, requested string, facts *Facts) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	data, truncated, readErr := content.ReadBounded(file, MaxInspectBytes)
	closeErr := file.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	facts.Encoding.ObservationPartial = truncated
	facts.Encoding.BOM = detectBOM(data)
	validUTF8 := utf8.Valid(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}))
	facts.Encoding.UTF8Valid = &validUTF8
	if validUTF8 {
		facts.Encoding.Candidates = append(facts.Encoding.Candidates, "utf-8")
	}
	if _, err := textcodec.Decode("euc-kr", data); err == nil {
		facts.Encoding.Candidates = append(facts.Encoding.Candidates, "euc-kr")
	}
	if _, err := textcodec.Decode("cp949", data); err == nil {
		facts.Encoding.Candidates = append(facts.Encoding.Candidates, "cp949")
	}
	if requested != "" {
		normalized, err := textcodec.Normalize(requested)
		if err != nil {
			return err
		}
		facts.Encoding.Requested = normalized
		if truncated {
			return nil
		}
		_, decodeErr := textcodec.Decode(normalized, data)
		success := decodeErr == nil
		facts.Encoding.DecodeSuccess = &success
		if success {
			roundTrip, err := textcodec.RoundTrips(normalized, data)
			if err != nil {
				return err
			}
			facts.Encoding.ByteRoundTrip = &roundTrip
		}
	}
	facts.Newlines = countNewlines(data, truncated)
	if !truncated {
		hash := sha256.Sum256(data)
		facts.ContentHash = "sha256:" + hex.EncodeToString(hash[:])
	}
	return nil
}

func detectBOM(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return "utf-8"
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return "utf-16le"
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return "utf-16be"
	default:
		return "none"
	}
}

func countNewlines(data []byte, partial bool) Newlines {
	crlf := uint64(bytes.Count(data, []byte("\r\n")))
	lf := uint64(bytes.Count(data, []byte("\n"))) - crlf
	cr := uint64(bytes.Count(data, []byte("\r"))) - crlf
	result := Newlines{CRLF: crlf, LF: lf, CR: cr, Partial: partial}
	if !partial {
		final := len(data) > 0 && (data[len(data)-1] == '\n' || data[len(data)-1] == '\r')
		result.FinalNewline = &final
	}
	return result
}

func fileKind(info os.FileInfo) string {
	switch {
	case info.Mode().IsRegular():
		return "file"
	case info.IsDir():
		return "directory"
	case info.Mode()&os.ModeSymlink != 0:
		return "symlink"
	default:
		return "other"
	}
}

func (s *Service) rulesFor(path string, exists bool) []Rule {
	directory := path
	if exists {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			directory = filepath.Dir(path)
		}
	} else {
		directory = filepath.Dir(path)
		for {
			if _, err := os.Stat(directory); err == nil {
				break
			}
			next := filepath.Dir(directory)
			if next == directory {
				break
			}
			directory = next
		}
	}
	relative, err := filepath.Rel(s.root.Resolved(), directory)
	if err != nil {
		return make([]Rule, 0)
	}
	parts := []string{"."}
	if relative != "." {
		current := ""
		for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
			current = filepath.Join(current, part)
			parts = append(parts, current)
		}
	}
	rules := make([]Rule, 0)
	for _, scope := range parts {
		for _, name := range s.ruleFiles {
			candidate := filepath.Join(s.root.Resolved(), scope, name)
			info, err := os.Stat(candidate)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			rel, _ := filepath.Rel(s.root.Resolved(), candidate)
			scopePath := filepath.ToSlash(filepath.Clean(scope))
			rules = append(rules, Rule{
				Path: filepath.ToSlash(rel), ScopeRoot: scopePath,
				SourceRef: "source:mutable:" + filepath.ToSlash(rel),
			})
		}
	}
	return rules
}

func (s *Service) basenameCandidates(ctx context.Context, basename string) ([]string, bool, []string) {
	outcome, err := s.search.Search(ctx, search.BackendQuery{
		Mode: "filename", Pattern: basename, Directory: s.root.Resolved(), Hidden: true,
		Ignored: "include", Generated: "include", Vendor: "include", Encoding: "utf-8", CaseSensitive: runtime.GOOS != "windows",
	})
	if err != nil {
		return make([]string, 0), false, []string{"basename scan failed"}
	}
	result := make([]string, 0)
	for _, candidate := range outcome.Matches {
		equal := filepath.Base(filepath.FromSlash(candidate.Path)) == basename
		if runtime.GOOS == "windows" {
			equal = strings.EqualFold(filepath.Base(filepath.FromSlash(candidate.Path)), basename)
		}
		if equal {
			result = append(result, candidate.Path)
		}
	}
	return result, outcome.Complete, outcome.Warnings
}

func inspectGit(ctx context.Context, root *workspace.Root, path string, exists bool) GitFacts {
	directory := path
	if !exists {
		directory = filepath.Dir(path)
	}
	if info, err := os.Stat(directory); err == nil && !info.IsDir() {
		directory = filepath.Dir(directory)
	}
	for {
		if _, err := os.Stat(directory); err == nil {
			break
		}
		next := filepath.Dir(directory)
		if next == directory {
			return GitFacts{State: "not_applicable"}
		}
		directory = next
	}
	repositoryRoot, err := gitOutput(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitFacts{State: "not_applicable"}
	}
	repositoryRoot = filepath.Clean(repositoryRoot)
	repositoryRelative, err := filepath.Rel(root.Resolved(), repositoryRoot)
	if err != nil || strings.HasPrefix(repositoryRelative, "..") {
		return GitFacts{State: "unknown"}
	}
	gitDir, err := gitOutput(ctx, repositoryRoot, "rev-parse", "--git-dir")
	if err != nil {
		return GitFacts{State: "unknown"}
	}
	idHash := sha256.Sum256([]byte(repositoryRoot + "\x00" + gitDir))
	rel, err := filepath.Rel(repositoryRoot, path)
	if err != nil {
		return GitFacts{State: "unknown"}
	}
	tracked := gitSuccess(ctx, repositoryRoot, "ls-files", "--error-unmatch", "--", rel)
	ignored := gitSuccess(ctx, repositoryRoot, "check-ignore", "-q", "--", rel)
	untracked := exists && !tracked && !ignored
	return GitFacts{
		State: "observed", RepositoryRoot: filepath.ToSlash(repositoryRelative),
		WorktreeID: "git_" + hex.EncodeToString(idHash[:16]),
		Tracked:    &tracked, Ignored: &ignored, Untracked: &untracked,
	}
}

func gitOutput(ctx context.Context, directory string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, arguments...)...)
	output := &boundedWriter{limit: 16 * 1024}
	command.Stdout = output
	command.Stderr = nil
	if err := command.Run(); err != nil {
		return "", err
	}
	if output.truncated {
		return "", errors.New("git output exceeds limit")
	}
	return strings.TrimSpace(output.String()), nil
}

type boundedWriter struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (w *boundedWriter) Write(data []byte) (int, error) {
	original := len(data)
	remaining := w.limit - w.buffer.Len()
	if remaining <= 0 {
		w.truncated = true
		return original, nil
	}
	if len(data) > remaining {
		_, _ = w.buffer.Write(data[:remaining])
		w.truncated = true
		return original, nil
	}
	_, _ = w.buffer.Write(data)
	return original, nil
}

func (w *boundedWriter) String() string { return w.buffer.String() }

func gitSuccess(ctx context.Context, directory string, arguments ...string) bool {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, arguments...)...)
	command.Stdout = nil
	command.Stderr = nil
	return command.Run() == nil
}
