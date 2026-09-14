package search

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"repoplane/internal/contracts"
	"repoplane/internal/cursor"
	"repoplane/internal/store"
	"repoplane/internal/workspace"
)

const resultTTL = 30 * time.Minute

var ErrResponseTooLarge = errors.New("search: response cannot fit byte_limit")

type Request struct {
	Mode          string   `json:"mode,omitempty" jsonschema:"search mode: filename, exact, or regex; omit when using cursor"`
	Pattern       string   `json:"pattern,omitempty" jsonschema:"filename substring, exact text, or regular expression; omit when using cursor"`
	Root          string   `json:"root,omitempty" jsonschema:"workspace-relative search root; defaults to ."`
	Include       []string `json:"include,omitempty" jsonschema:"ripgrep glob patterns to include"`
	Exclude       []string `json:"exclude,omitempty" jsonschema:"ripgrep glob patterns to exclude"`
	Hidden        bool     `json:"hidden,omitempty" jsonschema:"include hidden paths"`
	Ignored       string   `json:"ignored,omitempty" jsonschema:"ignored path policy: exclude or include"`
	Generated     string   `json:"generated,omitempty" jsonschema:"generated path policy: exclude or include"`
	Vendor        string   `json:"vendor,omitempty" jsonschema:"vendor path policy: exclude or include"`
	Encoding      string   `json:"encoding,omitempty" jsonschema:"text encoding: utf-8, cp949, or euc-kr"`
	CaseSensitive bool     `json:"case_sensitive,omitempty" jsonschema:"perform case-sensitive matching"`
	Cursor        string   `json:"cursor,omitempty" jsonschema:"opaque cursor from an earlier workspace_search"`
	ItemLimit     uint64   `json:"item_limit,omitempty" jsonschema:"item limit; default 50, max 500"`
	ByteLimit     uint64   `json:"byte_limit,omitempty" jsonschema:"response bytes; default 65536, max 1048576"`
	TimeLimitMS   int64    `json:"time_limit_ms,omitempty" jsonschema:"deadline ms; default 5000, max 30000"`
}

type Result struct {
	Path      string `json:"path"`
	Line      uint64 `json:"line,omitempty"`
	Text      string `json:"text,omitempty"`
	SourceRef string `json:"source_ref"`
	Basis     string `json:"basis"`
}

type Scope struct {
	Root          string   `json:"root"`
	Include       []string `json:"include"`
	Exclude       []string `json:"exclude"`
	Hidden        bool     `json:"hidden"`
	Ignored       string   `json:"ignored"`
	Generated     string   `json:"generated"`
	Vendor        string   `json:"vendor"`
	Encoding      string   `json:"encoding"`
	CaseSensitive bool     `json:"case_sensitive"`
}

type Metadata struct {
	Engine string `json:"engine"`
	Mode   string `json:"mode"`
	Scope  Scope  `json:"scope"`
}

type Response struct {
	Status      contracts.Status    `json:"status"`
	Items       []Result            `json:"items"`
	Counts      contracts.Counts    `json:"counts"`
	Scan        contracts.Scan      `json:"scan"`
	Truncated   *bool               `json:"truncated"`
	NextCursor  *string             `json:"next_cursor"`
	SnapshotRef *string             `json:"snapshot_ref"`
	Warnings    []contracts.Warning `json:"warnings"`
	Metadata    Metadata            `json:"metadata"`
}

type persistedMetadata struct {
	Status   contracts.Status        `json:"status"`
	Relation contracts.CountRelation `json:"relation"`
	Scan     contracts.Scan          `json:"scan"`
	Warnings []contracts.Warning     `json:"warnings"`
	Metadata Metadata                `json:"metadata"`
}

type Service struct {
	root       *workspace.Root
	repository store.Repository
	cursors    *cursor.Codec
	backend    Backend
	now        func() time.Time
}

func NewService(root *workspace.Root, repository store.Repository, cursors *cursor.Codec, backend Backend) *Service {
	return &Service{root: root, repository: repository, cursors: cursors, backend: backend, now: time.Now}
}

func (s *Service) Query(ctx context.Context, request Request) (Response, error) {
	limits, err := contracts.NormalizeLimits(contracts.LimitRequest{
		ItemLimit: request.ItemLimit, ByteLimit: request.ByteLimit, TimeLimitMS: request.TimeLimitMS,
	})
	if err != nil {
		return Response{}, err
	}
	if request.Cursor != "" {
		return s.fromCursor(ctx, request.Cursor, limits.ItemLimit, limits.ByteLimit)
	}
	request = normalizeRequest(request)
	if err := validateRequest(request); err != nil {
		if strings.Contains(err.Error(), "encoding") {
			return unsupportedResponse(request, err), nil
		}
		return Response{}, err
	}
	resolvedRoot, err := s.root.ResolveExisting(filepath.FromSlash(request.Root))
	if err != nil {
		return Response{}, err
	}
	scope := Scope{
		Root: filepath.ToSlash(filepath.Clean(request.Root)), Include: nonNil(request.Include), Exclude: nonNil(request.Exclude),
		Hidden: request.Hidden, Ignored: request.Ignored, Generated: request.Generated,
		Vendor: request.Vendor, Encoding: request.Encoding, CaseSensitive: request.CaseSensitive,
	}
	scopeJSON, _ := json.Marshal(scope)
	scopeHash := sha256.Sum256(scopeJSON)
	scopeRef := "scope:sha256:" + hex.EncodeToString(scopeHash[:])

	scanCtx, cancel := context.WithTimeout(ctx, limits.TimeLimit)
	outcome, err := s.backend.Search(scanCtx, BackendQuery{
		Mode: request.Mode, Pattern: request.Pattern, Directory: resolvedRoot,
		Includes: request.Include, Excludes: request.Exclude, Hidden: request.Hidden,
		Ignored: request.Ignored, Generated: request.Generated, Vendor: request.Vendor,
		CaseSensitive: request.CaseSensitive, Encoding: request.Encoding,
	})
	cancel()
	if err != nil {
		return Response{}, err
	}
	status := contracts.StatusOK
	relation := contracts.CountExact
	scanState := contracts.ScanComplete
	if !outcome.Complete {
		status = contracts.StatusPartial
		relation = contracts.CountLowerBound
		scanState = contracts.ScanPartial
	}
	warnings := make([]contracts.Warning, 0, len(outcome.Warnings)+1)
	for _, message := range outcome.Warnings {
		warnings = append(warnings, contracts.Warning{Code: "backend_warning", Message: message})
	}
	metadata := Metadata{Engine: outcome.Engine, Mode: request.Mode, Scope: scope}
	persisted := persistedMetadata{
		Status: status, Relation: relation, Scan: contracts.Scan{State: scanState, ScopeRef: &scopeRef},
		Warnings: warnings, Metadata: metadata,
	}
	return s.persist(ctx, request, outcome.Matches, persisted, limits.ItemLimit, limits.ByteLimit)
}

func normalizeRequest(request Request) Request {
	if request.Root == "" {
		request.Root = "."
	}
	if request.Ignored == "" {
		request.Ignored = "exclude"
	}
	if request.Generated == "" {
		request.Generated = "exclude"
	}
	if request.Vendor == "" {
		request.Vendor = "exclude"
	}
	if request.Encoding == "" {
		request.Encoding = "utf-8"
	}
	return request
}

func validateRequest(request Request) error {
	switch request.Mode {
	case "filename", "exact", "regex":
	default:
		return fmt.Errorf("unsupported search mode %q", request.Mode)
	}
	if request.Pattern == "" && request.Mode != "filename" {
		return errors.New("pattern is required for exact and regex search")
	}
	for name, value := range map[string]string{
		"ignored": request.Ignored, "generated": request.Generated, "vendor": request.Vendor,
	} {
		if value != "exclude" && value != "include" {
			return fmt.Errorf("%s must be exclude or include", name)
		}
	}
	if request.Mode != "filename" && request.Encoding != "utf-8" && request.Encoding != "cp949" && request.Encoding != "euc-kr" {
		return fmt.Errorf("encoding %q is not supported for text search", request.Encoding)
	}
	return nil
}

func unsupportedResponse(request Request, reason error) Response {
	unknown := (*uint64)(nil)
	return Response{
		Status: contracts.StatusUnsupported, Items: make([]Result, 0),
		Counts:   contracts.Counts{Matched: unknown, Relation: contracts.CountUnknown},
		Scan:     contracts.Scan{State: contracts.ScanNotApplicable},
		Warnings: []contracts.Warning{{Code: "unsupported_encoding", Message: reason.Error()}},
		Metadata: Metadata{Mode: request.Mode, Scope: Scope{Encoding: request.Encoding}},
	}
}

func (s *Service) persist(ctx context.Context, request Request, matches []Match, metadata persistedMetadata, itemLimit, byteLimit uint64) (Response, error) {
	setID, err := searchID()
	if err != nil {
		return Response{}, err
	}
	now := s.now().UTC()
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return Response{}, err
	}
	request.Cursor = ""
	requestJSON, _ := json.Marshal(request)
	queryHash := sha256.Sum256(requestJSON)
	set := store.ResultSet{
		ID: setID, WorkspaceID: s.root.ID(), QueryHash: "sha256:" + hex.EncodeToString(queryHash[:]),
		GenerationID: "workspace-search-v1", CreatedAt: now, ExpiresAt: now.Add(resultTTL),
		Metadata: metaJSON, Items: make([]store.ResultItem, 0, len(matches)),
	}
	for index, match := range matches {
		workspacePath := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.FromSlash(request.Root), filepath.FromSlash(match.Path))))
		workspacePath = strings.TrimPrefix(workspacePath, "./")
		result := Result{
			Path: workspacePath, Line: match.Line, Text: match.Text,
			SourceRef: "source:mutable:" + workspacePath, Basis: string(contracts.BasisObserved),
		}
		payload, _ := json.Marshal(result)
		hash := sha256.Sum256(payload)
		set.Items = append(set.Items, store.ResultItem{
			Ordinal: uint64(index), ItemRef: result.SourceRef,
			ItemHash: "sha256:" + hex.EncodeToString(hash[:]), Payload: payload,
		})
	}
	if err := s.repository.CreateResultSet(ctx, set); err != nil {
		return Response{}, err
	}
	return s.readPage(ctx, set.ID, 0, itemLimit, byteLimit, set.ExpiresAt)
}

func (s *Service) fromCursor(ctx context.Context, encoded string, itemLimit, byteLimit uint64) (Response, error) {
	payload, err := s.cursors.Decode(encoded, s.root.ID())
	if err != nil {
		return Response{}, err
	}
	return s.readPage(ctx, payload.ResultSetID, payload.NextOrdinal, itemLimit, byteLimit, time.Unix(payload.ExpiresUnix, 0))
}

func (s *Service) readPage(ctx context.Context, setID string, from, itemLimit, byteLimit uint64, expires time.Time) (Response, error) {
	page, err := s.repository.ReadResultPage(ctx, setID, from, itemLimit)
	if err != nil {
		return Response{}, err
	}
	var metadata persistedMetadata
	if err := json.Unmarshal(page.Metadata, &metadata); err != nil {
		return Response{}, fmt.Errorf("decode search metadata: %w", err)
	}
	items := make([]Result, 0, len(page.Items))
	for _, stored := range page.Items {
		var item Result
		if err := json.Unmarshal(stored.Payload, &item); err != nil {
			return Response{}, fmt.Errorf("decode search result: %w", err)
		}
		items = append(items, item)
	}
	for returned := len(items); returned >= 0; returned-- {
		nextOrdinal := from + uint64(returned)
		knownMore := nextOrdinal < page.Total
		var nextCursor *string
		if knownMore {
			value, err := s.cursors.Encode(s.root.ID(), setID, nextOrdinal, expires)
			if err != nil {
				return Response{}, err
			}
			nextCursor = &value
		}
		var truncated *bool
		switch {
		case knownMore:
			value := true
			truncated = &value
		case metadata.Scan.State == contracts.ScanPartial:
			truncated = nil
		default:
			value := false
			truncated = &value
		}
		snapshot := "snapshot:" + setID
		matched := page.Total
		response := Response{
			Status: metadata.Status, Items: items[:returned],
			Counts: contracts.Counts{Matched: &matched, Relation: metadata.Relation, Returned: uint64(returned)},
			Scan:   metadata.Scan, Truncated: truncated, NextCursor: nextCursor, SnapshotRef: &snapshot,
			Warnings: metadata.Warnings, Metadata: metadata.Metadata,
		}
		encoded, err := json.Marshal(response)
		if err == nil && uint64(len(encoded)) <= byteLimit {
			if returned == 0 && len(items) > 0 {
				return Response{}, ErrResponseTooLarge
			}
			return response, nil
		}
	}
	return Response{}, ErrResponseTooLarge
}

func searchID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "search_" + hex.EncodeToString(data), nil
}

func nonNil(values []string) []string {
	if values == nil {
		return make([]string, 0)
	}
	return values
}
