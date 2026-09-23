package catalog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"repoplane/internal/contracts"
	"repoplane/internal/cursor"
	"repoplane/internal/store"
)

const resultSetTTL = 30 * time.Minute

var ErrResponseTooLarge = errors.New("catalog: response cannot fit byte_limit")

// MissingCapabilityError tells an MCP client how to inspect registration without
// pretending that an unspecified private host profile can be discovered.
type MissingCapabilityError struct{ CandidateRoots []string }

func (e *MissingCapabilityError) Error() string {
	return "catalog capability is not registered in configured roots"
}
func (e *MissingCapabilityError) Unwrap() error { return store.ErrNotFound }

func (s *Service) missingCapability() error {
	return &MissingCapabilityError{CandidateRoots: s.indexer.unconfiguredCatalogRoots()}
}

type QueryRequest struct {
	Mode        string `json:"mode,omitempty" jsonschema:"omit with cursor"`
	Query       string `json:"query,omitempty" jsonschema:"lexical search text; required for search"`
	ID          string `json:"id,omitempty" jsonschema:"catalog item ID; required for get"`
	Cursor      string `json:"cursor,omitempty" jsonschema:"opaque cursor returned by an earlier list or search"`
	ItemLimit   uint64 `json:"item_limit,omitempty" jsonschema:"item limit; default 50, max 500"`
	ByteLimit   uint64 `json:"byte_limit,omitempty" jsonschema:"response bytes; default 65536, max 1048576"`
	TimeLimitMS int64  `json:"time_limit_ms,omitempty" jsonschema:"deadline ms; default 5000, max 30000"`
}

type QueryResult struct {
	Kind               string            `json:"kind"`
	ID                 string            `json:"id,omitempty"`
	Revision           string            `json:"revision,omitempty"`
	Summary            string            `json:"summary,omitempty"`
	MatchReason        string            `json:"match_reason,omitempty"`
	ExecutionAvailable bool              `json:"execution_available"`
	SourceRef          string            `json:"source_ref,omitempty"`
	Manifest           *Manifest         `json:"manifest,omitempty"`
	IssueCode          string            `json:"issue_code,omitempty"`
	Detail             map[string]string `json:"detail,omitempty"`
	GenerationID       string            `json:"generation_id,omitempty"`
	SourceFingerprint  string            `json:"source_fingerprint,omitempty"`
	IssueCount         uint64            `json:"issue_count,omitempty"`
}

type QueryResponse = contracts.Response[QueryResult]

type Service struct {
	workspaceID      string
	repository       store.Repository
	indexer          *Indexer
	cursors          *cursor.Codec
	now              func() time.Time
	executionEnabled bool
	refreshMu        sync.Mutex
}

// EnableExecution marks trusted declarations available for this host process.
// It must be called during application composition, before serving requests.
func (s *Service) EnableExecution() { s.executionEnabled = true }

type Capability struct {
	Manifest             Manifest
	Revision             string
	SourceRef            string
	ExecutionFingerprint string
	GenerationID         string
}

func NewService(workspaceID string, repository store.Repository, indexer *Indexer, cursors *cursor.Codec) *Service {
	return &Service{
		workspaceID: workspaceID,
		repository:  repository,
		indexer:     indexer,
		cursors:     cursors,
		now:         time.Now,
	}
}

// ResolveCapability refreshes the catalog and returns one immutable execution declaration.
func (s *Service) ResolveCapability(ctx context.Context, id string) (Capability, error) {
	s.refreshMu.Lock()
	meta, err := s.indexer.Refresh(ctx)
	s.refreshMu.Unlock()
	if err != nil {
		return Capability{}, fmt.Errorf("refresh catalog: %w", err)
	}
	item, err := s.repository.GetCatalogItem(ctx, s.workspaceID, meta.ID, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Capability{}, s.missingCapability()
		}
		return Capability{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(item.Document, &manifest); err != nil {
		return Capability{}, fmt.Errorf("decode stored manifest %q: %w", item.ID, err)
	}
	return Capability{Manifest: manifest, Revision: item.Revision, SourceRef: item.SourceRef, ExecutionFingerprint: item.ExecutionFingerprint, GenerationID: meta.ID}, nil
}

func (s *Service) Query(ctx context.Context, request QueryRequest) (QueryResponse, error) {
	if request.Mode == "" && request.Cursor == "" {
		return QueryResponse{}, errors.New("mode required")
	}
	limits, err := contracts.NormalizeLimits(contracts.LimitRequest{
		ItemLimit: request.ItemLimit, ByteLimit: request.ByteLimit, TimeLimitMS: request.TimeLimitMS,
	})
	if err != nil {
		return QueryResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, limits.TimeLimit)
	defer cancel()

	if request.Cursor != "" {
		if request.Mode != "" && request.Mode != "search" && request.Mode != "list" {
			return QueryResponse{}, errors.New("cursor is only valid for search or list")
		}
		return s.pageFromCursor(ctx, request.Cursor, limits.ItemLimit, limits.ByteLimit)
	}

	s.refreshMu.Lock()
	meta, err := s.indexer.Refresh(ctx)
	s.refreshMu.Unlock()
	if err != nil {
		return QueryResponse{}, fmt.Errorf("refresh catalog: %w", err)
	}

	switch request.Mode {
	case "search":
		if strings.TrimSpace(request.Query) == "" {
			return QueryResponse{}, errors.New("query is required for search")
		}
		return s.search(ctx, meta, request.Query, limits.ItemLimit, limits.ByteLimit)
	case "list":
		return s.search(ctx, meta, "", limits.ItemLimit, limits.ByteLimit)
	case "get":
		if request.ID == "" {
			return QueryResponse{}, errors.New("id is required for get")
		}
		return s.get(ctx, meta, request.ID, limits.ByteLimit)
	case "audit":
		return s.audit(ctx, meta, limits.ItemLimit, limits.ByteLimit)
	case "status":
		return s.status(ctx, meta, limits.ByteLimit)
	default:
		return QueryResponse{}, fmt.Errorf("unsupported catalog mode %q", request.Mode)
	}
}

func (s *Service) search(ctx context.Context, meta store.CatalogGenerationMeta, query string, itemLimit, byteLimit uint64) (QueryResponse, error) {
	page, err := s.repository.SearchCatalog(ctx, store.CatalogQuery{
		WorkspaceID:  s.workspaceID,
		GenerationID: meta.ID,
		ExactID:      strings.TrimSpace(query),
		Terms:        strings.Fields(query),
		Limit:        MaxManifestFiles,
	})
	if err != nil {
		return QueryResponse{}, err
	}
	results := make([]QueryResult, 0, len(page.Matches))
	for _, match := range page.Matches {
		result, err := queryResult(match.Item, false, s.executionEnabled)
		if err != nil {
			return QueryResponse{}, err
		}
		if match.Score >= 1_000_000 {
			result.MatchReason = "exact_id"
		} else if query != "" {
			result.MatchReason = "lexical"
		} else {
			result.MatchReason = "list"
		}
		results = append(results, result)
	}
	response, err := s.persistAndReadFirstPage(ctx, meta, query, results, itemLimit, byteLimit)
	if err != nil {
		return QueryResponse{}, err
	}
	if query != "" && len(results) == 0 {
		response.Warnings = append(response.Warnings, contracts.Warning{
			Code: "catalog_no_match", Message: "No registered match. Call catalog_query(mode=status), then runtime_config(action=status) to inspect configured and candidate catalog roots.",
		})
		if !responseFits(response, byteLimit) {
			return QueryResponse{}, ErrResponseTooLarge
		}
	}
	return response, nil
}

func (s *Service) get(ctx context.Context, meta store.CatalogGenerationMeta, id string, byteLimit uint64) (QueryResponse, error) {
	item, err := s.repository.GetCatalogItem(ctx, s.workspaceID, meta.ID, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return QueryResponse{}, s.missingCapability()
		}
		return QueryResponse{}, err
	}
	result, err := queryResult(item, true, s.executionEnabled)
	if err != nil {
		return QueryResponse{}, err
	}
	one := uint64(1)
	truncated := false
	snapshot := "snapshot:" + meta.ID
	response := QueryResponse{
		Status: contracts.StatusOK, Items: []QueryResult{result},
		Counts: contracts.Counts{Matched: &one, Relation: contracts.CountExact, Returned: 1},
		Scan:   contracts.Scan{State: contracts.ScanNotApplicable}, Truncated: &truncated,
		SnapshotRef: &snapshot, Warnings: contracts.EmptyWarnings(),
	}
	if !responseFits(response, byteLimit) {
		return QueryResponse{}, ErrResponseTooLarge
	}
	return response, nil
}

func (s *Service) catalogRootWarnings() []contracts.Warning {
	candidates := s.indexer.unconfiguredCatalogRoots()
	warnings := make([]contracts.Warning, 0, len(candidates))
	for _, candidate := range candidates {
		ref := candidate
		warnings = append(warnings, contracts.Warning{
			Code: "catalog_candidate_unconfigured", Message: "catalog candidate is outside the configured catalog_root values; inspect runtime_config before changing sources", Ref: &ref,
		})
	}
	return warnings
}

func (s *Service) audit(ctx context.Context, meta store.CatalogGenerationMeta, itemLimit, byteLimit uint64) (QueryResponse, error) {
	issues, err := s.repository.ListCatalogIssues(ctx, s.workspaceID, meta.ID)
	if err != nil {
		return QueryResponse{}, err
	}
	results := make([]QueryResult, 0, len(issues))
	for _, issue := range issues {
		var detail map[string]string
		if err := json.Unmarshal(issue.Detail, &detail); err != nil {
			return QueryResponse{}, fmt.Errorf("decode catalog issue: %w", err)
		}
		results = append(results, QueryResult{
			Kind: "issue", SourceRef: issue.SourceRef, IssueCode: issue.Code, Detail: detail,
		})
	}
	return s.persistAndReadFirstPage(ctx, meta, "audit", results, itemLimit, byteLimit)
}

func (s *Service) status(ctx context.Context, meta store.CatalogGenerationMeta, byteLimit uint64) (QueryResponse, error) {
	page, err := s.repository.SearchCatalog(ctx, store.CatalogQuery{
		WorkspaceID: s.workspaceID, GenerationID: meta.ID, Limit: MaxManifestFiles,
	})
	if err != nil {
		return QueryResponse{}, err
	}
	issues, err := s.repository.ListCatalogIssues(ctx, s.workspaceID, meta.ID)
	if err != nil {
		return QueryResponse{}, err
	}
	one := uint64(1)
	truncated := false
	snapshot := "snapshot:" + meta.ID
	count := uint64(len(page.Matches))
	response := QueryResponse{
		Status: contracts.StatusOK,
		Items: []QueryResult{{
			Kind: "status", GenerationID: meta.ID, SourceFingerprint: meta.SourceFingerprint,
			Summary: fmt.Sprintf("%d catalog items", count), IssueCount: uint64(len(issues)),
		}},
		Counts: contracts.Counts{Matched: &one, Relation: contracts.CountExact, Returned: 1},
		Scan:   contracts.Scan{State: contracts.ScanComplete}, Truncated: &truncated,
		SnapshotRef: &snapshot, Warnings: s.catalogRootWarnings(),
	}
	if !responseFits(response, byteLimit) {
		return QueryResponse{}, ErrResponseTooLarge
	}
	return response, nil
}

func (s *Service) persistAndReadFirstPage(ctx context.Context, meta store.CatalogGenerationMeta, query string, results []QueryResult, itemLimit, byteLimit uint64) (QueryResponse, error) {
	now := s.now().UTC()
	setID, err := randomID("results_")
	if err != nil {
		return QueryResponse{}, err
	}
	querySum := sha256.Sum256([]byte(meta.ID + "\x00" + query))
	set := store.ResultSet{
		ID: setID, WorkspaceID: s.workspaceID, QueryHash: "sha256:" + hex.EncodeToString(querySum[:]),
		GenerationID: meta.ID, CreatedAt: now, ExpiresAt: now.Add(resultSetTTL),
		Metadata: json.RawMessage(`{}`),
		Items:    make([]store.ResultItem, 0, len(results)),
	}
	for ordinal, result := range results {
		payload, err := json.Marshal(result)
		if err != nil {
			return QueryResponse{}, err
		}
		sum := sha256.Sum256(payload)
		set.Items = append(set.Items, store.ResultItem{
			Ordinal: uint64(ordinal), ItemRef: result.SourceRef,
			ItemHash: "sha256:" + hex.EncodeToString(sum[:]), Payload: payload,
		})
		if set.Items[len(set.Items)-1].ItemRef == "" {
			set.Items[len(set.Items)-1].ItemRef = "catalog-result:" + setID + ":" + fmt.Sprint(ordinal)
		}
	}
	if err := s.repository.CreateResultSet(ctx, set); err != nil {
		return QueryResponse{}, err
	}
	return s.readPage(ctx, setID, 0, itemLimit, byteLimit, set.ExpiresAt)
}

func (s *Service) pageFromCursor(ctx context.Context, encoded string, itemLimit, byteLimit uint64) (QueryResponse, error) {
	payload, err := s.cursors.Decode(encoded, s.workspaceID)
	if err != nil {
		return QueryResponse{}, err
	}
	return s.readPage(ctx, payload.ResultSetID, payload.NextOrdinal, itemLimit, byteLimit, time.Unix(payload.ExpiresUnix, 0))
}

func (s *Service) readPage(ctx context.Context, setID string, from, itemLimit, byteLimit uint64, expires time.Time) (QueryResponse, error) {
	page, err := s.repository.ReadResultPage(ctx, setID, from, itemLimit)
	if err != nil {
		return QueryResponse{}, err
	}
	items := make([]QueryResult, 0, len(page.Items))
	for _, stored := range page.Items {
		var result QueryResult
		if err := json.Unmarshal(stored.Payload, &result); err != nil {
			return QueryResponse{}, fmt.Errorf("decode stored result: %w", err)
		}
		items = append(items, result)
	}
	for returned := len(items); returned >= 0; returned-- {
		nextOrdinal := from + uint64(returned)
		hasMore := nextOrdinal < page.Total
		var nextCursor *string
		if hasMore {
			encoded, err := s.cursors.Encode(s.workspaceID, setID, nextOrdinal, expires)
			if err != nil {
				return QueryResponse{}, err
			}
			nextCursor = &encoded
		}
		truncated := hasMore
		snapshot := "snapshot:" + setID
		matched := page.Total
		response := QueryResponse{
			Status: contracts.StatusOK, Items: items[:returned],
			Counts: contracts.Counts{Matched: &matched, Relation: contracts.CountExact, Returned: uint64(returned)},
			Scan:   contracts.Scan{State: contracts.ScanComplete}, Truncated: &truncated,
			NextCursor: nextCursor, SnapshotRef: &snapshot, Warnings: contracts.EmptyWarnings(),
		}
		if responseFits(response, byteLimit) {
			if returned == 0 && len(items) > 0 {
				return QueryResponse{}, ErrResponseTooLarge
			}
			return response, nil
		}
	}
	return QueryResponse{}, ErrResponseTooLarge
}

func queryResult(item store.CatalogItem, includeManifest, executionEnabled bool) (QueryResult, error) {
	var manifest Manifest
	if err := json.Unmarshal(item.Document, &manifest); err != nil {
		return QueryResult{}, fmt.Errorf("decode stored manifest %q: %w", item.ID, err)
	}
	result := QueryResult{
		Kind: "catalog_item", ID: item.ID, Revision: item.Revision, Summary: manifest.Summary,
		ExecutionAvailable: executionEnabled && manifest.Execution != nil && manifest.Execution.TrustedForRun, SourceRef: item.SourceRef,
	}
	if includeManifest {
		result.Manifest = &manifest
	}
	return result, nil
}

func randomID(prefix string) (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate ID: %w", err)
	}
	return prefix + hex.EncodeToString(data), nil
}

func responseFits(response QueryResponse, byteLimit uint64) bool {
	encoded, err := json.Marshal(response)
	return err == nil && uint64(len(encoded)) <= byteLimit
}
