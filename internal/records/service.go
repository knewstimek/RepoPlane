// Package records implements durable project-record reads and narrowly scoped writes.
package records

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"repoplane/internal/contracts"
	"repoplane/internal/cursor"
	"repoplane/internal/store"
	"repoplane/internal/workspace"
)

const (
	resultSetTTL         = 30 * time.Minute
	maximumRecordQuery   = 1_000
	maximumReportBytes   = 1024 * 1024
	maximumRecordPayload = 64 * 1024
	parserRevision       = "check-report.v1-parser.1"
)

var (
	ErrResponseTooLarge  = errors.New("records: response cannot fit byte_limit")
	ErrPermissionDenied  = errors.New("records: permission denied")
	ErrReportInvalid     = errors.New("records: report invalid")
	ErrInvalidArgument   = errors.New("records: invalid argument")
	ErrInvalidTransition = errors.New("records: invalid transition")
	ErrStorageFailure    = errors.New("records: storage failure")
	checklistIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	gitCommitPattern     = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	hostAliasPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	topicKeyPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,127}$`)
)

// ValidationError identifies a safe request field and the rule it violated.
// It is suitable for returning to MCP clients without exposing submitted values.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return e.Field + " " + e.Reason }

type QueryRequest struct {
	Mode          string   `json:"mode,omitempty" jsonschema:"omit with cursor; inferred as search when query is set"`
	Query         string   `json:"query,omitempty" jsonschema:"space-separated lexical terms; search ranks coverage, resume defaults to all terms"`
	ID            string   `json:"id,omitempty" jsonschema:"opaque record ID; required for get"`
	Kind          string   `json:"kind,omitempty" jsonschema:"record kind filter: verification, checkpoint, memo, environment, run, or artifact"`
	Validity      string   `json:"validity,omitempty" jsonschema:"validity filter: current, stale, unknown, or superseded"`
	Source        string   `json:"source,omitempty" jsonschema:"source filter: observed, imported, user_asserted, or llm_proposed"`
	UpdatedAfter  string   `json:"updated_after,omitempty" jsonschema:"inclusive RFC3339 lower bound for update time"`
	UpdatedBefore string   `json:"updated_before,omitempty" jsonschema:"exclusive RFC3339 upper bound for update time"`
	TopicKey      string   `json:"topic_key,omitempty" jsonschema:"exact memo topic key; get_topic returns a current memo"`
	Scope         string   `json:"scope,omitempty" jsonschema:"exact memo scope filter"`
	Configuration string   `json:"configuration,omitempty" jsonschema:"exact memo configuration filter"`
	MatchMode     string   `json:"match_mode,omitempty" jsonschema:"search: any (default) or all; resume: all (default) or any"`
	ResponseView  string   `json:"response_view,omitempty" jsonschema:"brief, discovery (legacy search), or full; search defaults brief"`
	Cursor        string   `json:"cursor,omitempty" jsonschema:"opaque cursor from an earlier project_records query"`
	ItemLimit     uint64   `json:"item_limit,omitempty" jsonschema:"item limit; search default 8, otherwise 50; max 500"`
	ByteLimit     uint64   `json:"byte_limit,omitempty" jsonschema:"structured bytes; search/resume default 8192, get/get_topic default 1048576, others 65536"`
	TimeLimitMS   int64    `json:"time_limit_ms,omitempty" jsonschema:"deadline ms; default 5000, max 30000"`
	PayloadFields []string `json:"payload_fields,omitempty" jsonschema:"exact top-level payload fields to return; empty returns the full payload"`
}

type RecordResult struct {
	ID              string         `json:"id"`
	Kind            string         `json:"kind,omitempty"`
	SchemaVersion   string         `json:"schema_version,omitempty"`
	Revision        uint64         `json:"revision,omitempty"`
	Source          string         `json:"source,omitempty"`
	WriterClass     string         `json:"writer_class,omitempty"`
	Validity        string         `json:"validity,omitempty"`
	CreatedAt       time.Time      `json:"created_at,omitzero"`
	UpdatedAt       time.Time      `json:"updated_at,omitzero"`
	Title           string         `json:"title,omitempty"`
	Summary         string         `json:"summary,omitempty"`
	NextAction      string         `json:"next_action,omitempty"`
	ChangeSummary   string         `json:"change_summary,omitempty"`
	ChangeState     string         `json:"change_state,omitempty"`
	BackgroundRefs  []string       `json:"background_refs,omitempty"`
	RemainingChecks []string       `json:"remaining_checks,omitempty"`
	TopicKey        string         `json:"topic_key,omitempty"`
	Scope           string         `json:"scope,omitempty"`
	Configuration   string         `json:"configuration,omitempty"`
	TemporalKind    string         `json:"temporal_kind,omitempty"`
	AsOf            string         `json:"as_of,omitempty"`
	SearchAsOf      time.Time      `json:"search_as_of,omitzero"`
	Payload         map[string]any `json:"payload,omitempty"`
	PayloadComplete bool           `json:"payload_complete,omitempty"`
	EvidenceRefs    []string       `json:"evidence_refs,omitempty"`
	Supersedes      string         `json:"supersedes,omitempty"`
	SupersededBy    string         `json:"superseded_by,omitempty"`
}

type QueryResponse = contracts.Response[RecordResult]

type CheckpointRequest struct {
	Mode             string   `json:"mode" jsonschema:"mutation action"`
	ID               string   `json:"id,omitempty" jsonschema:"ID for update/supersede"`
	ExpectedRevision uint64   `json:"expected_revision,omitempty" jsonschema:"revision for update/supersede"`
	Goal             string   `json:"goal,omitempty" jsonschema:"task goal"`
	BaselineCommit   string   `json:"baseline_commit,omitempty" jsonschema:"baseline Git commit"`
	Dirty            *bool    `json:"dirty,omitempty" jsonschema:"baseline worktree dirty"`
	RunRefs          []string `json:"run_refs,omitempty" jsonschema:"related run or report refs"`
	RemainingChecks  []string `json:"remaining_checks,omitempty" jsonschema:"remaining checks"`
	NextAction       string   `json:"next_action,omitempty" jsonschema:"next intended action"`
	ChangeSummary    string   `json:"change_summary,omitempty" jsonschema:"completed or changed work since the previous handoff; not inferred from Git"`
	BackgroundRefs   []string `json:"background_refs,omitempty" jsonschema:"current memo or other background record refs for task recovery"`
	Risks            []string `json:"risks,omitempty" jsonschema:"unverified risks"`
	EvidenceRefs     []string `json:"evidence_refs,omitempty" jsonschema:"evidence refs"`
	ResponseView     string   `json:"response_view,omitempty" jsonschema:"receipt returns ID and revision by default; full includes payload"`
}

type MemoRequest struct {
	Mode                  string    `json:"mode" jsonschema:"mutation action"`
	ID                    string    `json:"id,omitempty" jsonschema:"ID for update/supersede"`
	ExpectedRevision      uint64    `json:"expected_revision,omitempty" jsonschema:"revision for update/supersede"`
	Supersedes            string    `json:"supersedes,omitempty" jsonschema:"current memo ID to replace atomically on create; requires expected_revision"`
	MemoKind              string    `json:"memo_kind,omitempty" jsonschema:"decision | failed_attempt | resolved_failure | limitation | host_fact"`
	Scope                 string    `json:"scope,omitempty" jsonschema:"logical scope"`
	Configuration         string    `json:"configuration,omitempty" jsonschema:"configuration"`
	TopicKey              string    `json:"topic_key,omitempty" jsonschema:"stable topic key"`
	Title                 string    `json:"title,omitempty" jsonschema:"short human-readable title for discovery"`
	Summary               string    `json:"summary,omitempty" jsonschema:"one-line summary for discovery"`
	TemporalKind          string    `json:"temporal_kind,omitempty" jsonschema:"historical_observation or current_guidance; omitted means unknown"`
	AsOf                  string    `json:"as_of,omitempty" jsonschema:"RFC3339 time when the observation or guidance was asserted"`
	Content               string    `json:"content,omitempty" jsonschema:"memo text"`
	Host                  *HostFact `json:"host,omitempty" jsonschema:"typed host details for host_fact memos"`
	InvalidationCondition string    `json:"invalidation_condition,omitempty" jsonschema:"staleness condition"`
	Source                string    `json:"source" jsonschema:"memo provenance"`
	EvidenceRefs          []string  `json:"evidence_refs,omitempty" jsonschema:"evidence refs"`
	ResponseView          string    `json:"response_view,omitempty" jsonschema:"receipt returns ID and revision by default; full includes payload"`
}

type HostFact struct {
	Alias       string   `json:"alias"`
	Role        string   `json:"role"`
	OS          string   `json:"os"`
	Tier        string   `json:"tier"`
	Services    []string `json:"services"`
	Paths       []string `json:"paths"`
	ConfirmedAt string   `json:"confirmed_at"`
}

type ImportRequest struct {
	Path          string `json:"path" jsonschema:"workspace-relative check-report.v1 path"`
	ChecklistPath string `json:"checklist_path" jsonschema:"workspace-relative verification-check.v1 YAML or JSON path"`
	Configuration string `json:"configuration" jsonschema:"declared checklist configuration being imported"`
	ByteLimit     uint64 `json:"byte_limit,omitempty" jsonschema:"maximum report bytes; default and maximum 1048576"`
	TimeLimitMS   int64  `json:"time_limit_ms,omitempty" jsonschema:"deadline ms; default 5000, max 30000"`
	ResponseView  string `json:"response_view,omitempty" jsonschema:"receipt returns ID and revision by default; full includes payload"`
}

type MutationResponse struct {
	Status    contracts.Status    `json:"status"`
	Record    RecordResult        `json:"record"`
	Duplicate bool                `json:"duplicate"`
	Warnings  []contracts.Warning `json:"warnings"`
}

type Service struct {
	root        *workspace.Root
	projectID   string
	workspaceID string
	records     store.RecordRepository
	results     store.ResultSetRepository
	cursors     *cursor.Codec
	now         func() time.Time
}

func NewService(root *workspace.Root, records store.RecordRepository, results store.ResultSetRepository, cursors *cursor.Codec) *Service {
	return &Service{root: root, projectID: root.ID(), workspaceID: root.ID(), records: records, results: results, cursors: cursors, now: time.Now}
}

func (s *Service) Query(ctx context.Context, request QueryRequest) (QueryResponse, error) {
	if request.Mode == "" && request.Cursor == "" {
		if strings.TrimSpace(request.Query) != "" {
			request.Mode = "search"
		} else if request.TopicKey != "" {
			request.Mode = "get_topic"
		} else {
			return QueryResponse{}, errors.New("mode required")
		}
	}
	if request.Mode == "search" || request.Mode == "resume" {
		if request.ItemLimit == 0 {
			request.ItemLimit = 8
		}
		if request.ByteLimit == 0 {
			request.ByteLimit = 8 * 1024
		}
	}
	if (request.Mode == "get" || request.Mode == "get_topic") && request.ByteLimit == 0 {
		request.ByteLimit = contracts.MaximumByteLimit
	}
	limits, err := contracts.NormalizeLimits(contracts.LimitRequest{ItemLimit: request.ItemLimit, ByteLimit: request.ByteLimit, TimeLimitMS: request.TimeLimitMS})
	if err != nil {
		return QueryResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, limits.TimeLimit)
	defer cancel()
	if request.Cursor != "" {
		if request.Mode != "" && request.Mode != "list" && request.Mode != "search" {
			return QueryResponse{}, errors.New("cursor is only valid for search or list")
		}
		return s.fromCursor(ctx, request.Cursor, limits.ItemLimit, limits.ByteLimit)
	}
	if err := validateFilters(request); err != nil {
		return QueryResponse{}, err
	}
	request.TopicKey = strings.ToLower(request.TopicKey)
	var current subjectObservation
	switch request.Mode {
	case "get":
		if request.ID == "" {
			return QueryResponse{}, errors.New("id is required for get")
		}
		record, err := s.records.GetRecord(ctx, s.projectID, s.workspaceID, request.ID)
		if err != nil {
			return QueryResponse{}, err
		}
		if record.Kind == "verification" {
			current = s.observeSubject(ctx)
		}
		result, err := s.result(record, &current, request.PayloadFields, "full")
		if err != nil {
			return QueryResponse{}, err
		}
		if record.Kind == "memo" && record.Validity == "superseded" {
			successors, err := s.records.QueryRecords(ctx, store.RecordQuery{ProjectID: s.projectID, WorkspaceID: s.workspaceID,
				Kind: "memo", Supersedes: record.ID, Limit: 1})
			if err != nil {
				return QueryResponse{}, err
			}
			if len(successors.Records) == 1 {
				result.SupersededBy = "record:" + successors.Records[0].ID
			}
		}
		response, err := singleResponse(result, limits.ByteLimit)
		if err != nil {
			return QueryResponse{}, err
		}
		if result.SupersededBy != "" {
			ref := result.SupersededBy
			response.Warnings = append(response.Warnings, contracts.Warning{
				Code: "memo_superseded", Message: "a successor memo exists; inspect it before applying this record", Ref: &ref,
			})
		}
		if record.Kind == "memo" {
			response.Warnings = append(response.Warnings, s.memoBasisWarnings(ctx, record)...)
		}
		encoded, _ := json.Marshal(response)
		if uint64(len(encoded)) > limits.ByteLimit {
			return QueryResponse{}, ErrResponseTooLarge
		}
		return response, nil
	case "get_topic":
		if request.TopicKey == "" {
			return QueryResponse{}, errors.New("topic_key is required for get_topic")
		}
		page, err := s.records.QueryRecords(ctx, store.RecordQuery{ProjectID: s.projectID, WorkspaceID: s.workspaceID,
			Kind: "memo", Validity: "current", TopicKey: request.TopicKey, Scope: request.Scope,
			Configuration: request.Configuration, Limit: maximumRecordQuery})
		if err != nil {
			return QueryResponse{}, err
		}
		if page.Matched == 0 {
			return QueryResponse{}, store.ErrNotFound
		}
		if page.Matched == 1 {
			result, err := s.result(page.Records[0], &current, request.PayloadFields, "full")
			if err != nil {
				return QueryResponse{}, err
			}
			response, err := singleResponse(result, limits.ByteLimit)
			if err != nil {
				return QueryResponse{}, err
			}
			response.Warnings = append(response.Warnings, s.memoBasisWarnings(ctx, page.Records[0])...)
			encoded, _ := json.Marshal(response)
			if uint64(len(encoded)) > limits.ByteLimit {
				return QueryResponse{}, ErrResponseTooLarge
			}
			return response, nil
		}
		items := make([]RecordResult, 0, min(len(page.Records), 8))
		for _, record := range page.Records[:min(len(page.Records), 8)] {
			result, err := s.result(record, &current, nil, "brief")
			if err != nil {
				return QueryResponse{}, err
			}
			items = append(items, result)
		}
		return ambiguousResponse(items, page.Matched, "memo_topic_ambiguous",
			"topic_key matches multiple current memos; provide scope and configuration", limits.ByteLimit)
	case "resume":
		var page store.RecordPage
		partialTerms := false
		if request.ID != "" {
			record, err := s.records.GetRecord(ctx, s.projectID, s.workspaceID, request.ID)
			if err != nil {
				return QueryResponse{}, err
			}
			if record.Kind != "checkpoint" {
				return QueryResponse{}, errors.New("resume id must name a checkpoint")
			}
			page = store.RecordPage{Records: []store.Record{record}, Matched: 1, Complete: true}
		} else {
			terms := normalizeRecordTerms(request.Query)
			query := store.RecordQuery{ProjectID: s.projectID, WorkspaceID: s.workspaceID,
				Kind: "checkpoint", Validity: "current", Terms: terms, MatchAll: request.MatchMode != "any", Limit: maximumRecordQuery}
			page, err = s.records.QueryRecords(ctx, query)
			if err != nil {
				return QueryResponse{}, err
			}
			if page.Matched == 0 && query.MatchAll && len(terms) > 1 {
				query.MatchAll = false
				page, err = s.records.QueryRecords(ctx, query)
				if err != nil {
					return QueryResponse{}, err
				}
				partialTerms = page.Matched > 0
			}
		}
		if page.Matched == 0 {
			return QueryResponse{}, store.ErrNotFound
		}
		if page.Matched == 1 && !partialTerms {
			item, err := resumeRecord(page.Records[0])
			if err != nil {
				return QueryResponse{}, err
			}
			return singleResponse(item, limits.ByteLimit)
		}
		items := make([]RecordResult, 0, min(len(page.Records), 5))
		for _, record := range page.Records[:min(len(page.Records), 5)] {
			item, err := s.result(record, &current, nil, "brief")
			if err != nil {
				return QueryResponse{}, err
			}
			items = append(items, item)
		}
		if partialTerms {
			return ambiguousResponse(items, page.Matched, "checkpoint_no_full_match",
				"no current checkpoint matches all goal terms; these candidates match some terms; refine the query or use an id", limits.ByteLimit)
		}
		return ambiguousResponse(items, page.Matched, "checkpoint_ambiguous",
			"multiple current checkpoints match; provide a more specific query or checkpoint id", limits.ByteLimit)
	case "list", "search":
		if request.Kind == "" || request.Kind == "verification" {
			current = s.observeSubject(ctx)
		}
		var updatedAfter time.Time
		if request.UpdatedAfter != "" {
			updatedAfter, err = time.Parse(time.RFC3339, request.UpdatedAfter)
			if err != nil {
				return QueryResponse{}, errors.New("updated_after must be RFC3339")
			}
		}
		var updatedBefore time.Time
		if request.UpdatedBefore != "" {
			updatedBefore, err = time.Parse(time.RFC3339, request.UpdatedBefore)
			if err != nil {
				return QueryResponse{}, errors.New("updated_before must be RFC3339")
			}
		}
		if !updatedBefore.IsZero() && !updatedAfter.IsZero() && !updatedAfter.Before(updatedBefore) {
			return QueryResponse{}, errors.New("updated_after must precede updated_before")
		}
		terms := []string(nil)
		if request.Mode == "search" {
			terms = normalizeRecordTerms(request.Query)
		}
		storedValidity := request.Validity
		if request.Kind == "" || request.Kind == "verification" {
			storedValidity = ""
		}
		page, err := s.records.QueryRecords(ctx, store.RecordQuery{ProjectID: s.projectID, WorkspaceID: s.workspaceID,
			Kind: request.Kind, Validity: storedValidity, Source: request.Source, UpdatedAfter: updatedAfter,
			UpdatedBefore: updatedBefore, TopicKey: request.TopicKey, Scope: request.Scope,
			Configuration: request.Configuration, Terms: terms, MatchAll: request.MatchMode == "all", Limit: maximumRecordQuery})
		if err != nil {
			return QueryResponse{}, err
		}
		items := make([]RecordResult, 0, len(page.Records))
		searchAsOf := s.now().UTC()
		view := request.ResponseView
		if view == "" {
			view = "full"
			if request.Mode == "search" && len(request.PayloadFields) == 0 {
				view = "brief"
			}
		}
		for _, record := range page.Records {
			result, err := s.result(record, &current, request.PayloadFields, view)
			if err != nil {
				return QueryResponse{}, err
			}
			if request.Mode == "search" && view == "brief" {
				result.SearchAsOf = searchAsOf
			}
			// Dynamic verification validity may differ from the stored filter.
			if request.Validity == "" || result.Validity == request.Validity {
				items = append(items, result)
			}
		}
		return s.persist(ctx, request, items, page.Complete, limits.ItemLimit, limits.ByteLimit)
	default:
		return QueryResponse{}, fmt.Errorf("unsupported records mode %q", request.Mode)
	}
}

// ResolveCacheQualifications returns current passed verification records for
// every requested checklist ID. It intentionally exposes only opaque refs to
// Runner, keeping dynamic Git validity in the Records service.
func (s *Service) ResolveCacheQualifications(ctx context.Context, capabilityID, configuration string, checkIDs []string) ([]string, bool, error) {
	if capabilityID == "" || configuration == "" || len(checkIDs) == 0 || len(checkIDs) > 64 {
		return []string{}, false, nil
	}
	wanted := make(map[string]struct{}, len(checkIDs))
	for _, id := range checkIDs {
		wanted[id] = struct{}{}
	}
	page, err := s.records.QueryRecords(ctx, store.RecordQuery{ProjectID: s.projectID, WorkspaceID: s.workspaceID, Kind: "verification", Limit: maximumRecordQuery})
	if err != nil {
		return nil, false, err
	}
	if !page.Complete {
		return []string{}, false, nil
	}
	current := s.observeSubject(ctx)
	found := make(map[string]string, len(wanted))
	for _, record := range page.Records {
		var payload map[string]any
		decoder := json.NewDecoder(bytes.NewReader(record.Payload))
		decoder.UseNumber()
		if decoder.Decode(&payload) != nil || validityFromPayload(payload, current) != "current" {
			continue
		}
		id, _ := payload["check_id"].(string)
		capability, _ := payload["capability"].(string)
		config, _ := payload["configuration"].(string)
		outcome, _ := payload["outcome"].(string)
		if _, ok := wanted[id]; ok && capability == capabilityID && config == configuration && outcome == "passed" {
			if _, exists := found[id]; !exists {
				found[id] = "record:" + record.ID
			}
		}
	}
	refs := make([]string, 0, len(checkIDs))
	for _, id := range checkIDs {
		ref, ok := found[id]
		if !ok {
			return refs, false, nil
		}
		refs = append(refs, ref)
	}
	return refs, true, nil
}

func (s *Service) WriteCheckpoint(ctx context.Context, request CheckpointRequest) (MutationResponse, error) {
	if request.Mode == "create" && (request.BaselineCommit == "" || request.Dirty == nil) {
		commit, dirty, observed := currentSubject(ctx, s.root.Resolved())
		if observed {
			if request.BaselineCommit == "" {
				request.BaselineCommit = commit
			}
			if request.Dirty == nil {
				request.Dirty = &dirty
			}
		}
	}
	if err := validateCheckpoint(request); err != nil {
		return MutationResponse{}, invalidArgument(err)
	}
	payload, _ := json.Marshal(map[string]any{"goal": request.Goal, "baseline_commit": request.BaselineCommit, "dirty": request.Dirty, "run_refs": nonNil(request.RunRefs), "remaining_checks": nonNil(request.RemainingChecks), "next_action": request.NextAction, "change_summary": request.ChangeSummary, "background_refs": nonNil(request.BackgroundRefs), "risks": nonNil(request.Risks)})
	if len(payload) > maximumRecordPayload {
		return MutationResponse{}, contracts.ErrLimitExceeded
	}
	record, err := s.mutate(ctx, "checkpoint", "checkpoint.v1", "user_asserted", request.Mode, request.ID, request.ExpectedRevision, payload, request.EvidenceRefs)
	return s.mutationResponse(record, false, request.ResponseView, err)
}

func (s *Service) WriteMemo(ctx context.Context, request MemoRequest) (MutationResponse, error) {
	if request.Source == "" {
		request.Source = "user_asserted"
	}
	request.TopicKey = strings.ToLower(request.TopicKey)
	if err := validateMemo(request); err != nil {
		return MutationResponse{}, invalidArgument(err)
	}
	if request.Host != nil {
		request.Host.Alias = strings.ToLower(request.Host.Alias)
		confirmedAt, _ := time.Parse(time.RFC3339, request.Host.ConfirmedAt)
		request.Host.ConfirmedAt = confirmedAt.UTC().Format(time.RFC3339)
	}
	if request.AsOf != "" {
		asOf, _ := time.Parse(time.RFC3339, request.AsOf)
		request.AsOf = asOf.UTC().Format(time.RFC3339)
	}
	payloadFields := map[string]any{"memo_kind": request.MemoKind, "scope": request.Scope, "configuration": request.Configuration, "content": request.Content, "invalidation_condition": request.InvalidationCondition}
	if request.TopicKey != "" {
		payloadFields["topic_key"] = request.TopicKey
	}
	for _, field := range []struct{ name, value string }{
		{"title", request.Title}, {"summary", request.Summary}, {"temporal_kind", request.TemporalKind}, {"as_of", request.AsOf},
	} {
		if field.value != "" {
			payloadFields[field.name] = field.value
		}
	}
	if request.Host != nil {
		payloadFields["host"] = request.Host
	}
	payload, _ := json.Marshal(payloadFields)
	if len(payload) > maximumRecordPayload {
		return MutationResponse{}, contracts.ErrLimitExceeded
	}
	schemaVersion := "memo.v1"
	if request.MemoKind == "host_fact" {
		schemaVersion = "memo.v2"
	} else if request.TopicKey != "" {
		schemaVersion = "memo.v3"
	}
	if request.Mode == "update" {
		current, err := s.records.GetRecord(ctx, s.projectID, s.workspaceID, request.ID)
		if err != nil {
			return MutationResponse{}, mutationStoreError(err)
		}
		if current.Kind != "memo" || current.SchemaVersion != schemaVersion {
			return MutationResponse{}, fmt.Errorf("%w: update cannot change memo schema; supersede the existing memo and create the new kind", ErrInvalidTransition)
		}
		if schemaVersion == "memo.v3" {
			var existing struct {
				Scope         string `json:"scope"`
				Configuration string `json:"configuration"`
				TopicKey      string `json:"topic_key"`
			}
			if json.Unmarshal(current.Payload, &existing) != nil || existing.Scope != request.Scope || existing.Configuration != request.Configuration || existing.TopicKey != request.TopicKey {
				return MutationResponse{}, fmt.Errorf("%w: memo topic identity cannot change; supersede the existing memo and create a new one", ErrInvalidTransition)
			}
		}
	}
	warnings := s.hostFactWarnings(ctx, request)
	if request.Mode == "create" && request.TopicKey != "" && request.Supersedes == "" {
		if existing, ok, err := s.currentTopicMemo(ctx, request.Scope, request.Configuration, request.TopicKey); err != nil {
			warnings = append(warnings, contracts.Warning{Code: "memo_topic_check_unknown", Message: "current memo topics could not be checked"})
		} else if ok {
			return s.existingTopicResponse(existing, request.ResponseView)
		} else {
			warnings = append(warnings, s.relatedTopicWarnings(ctx, request)...)
		}
	}
	var record store.Record
	var err error
	if request.Mode == "create" && request.Supersedes != "" {
		newID, idErr := randomID("memo_")
		if idErr != nil {
			return MutationResponse{}, idErr
		}
		now := s.now().UTC()
		record, err = s.records.ReplaceMemo(ctx, store.RecordReplace{
			ProjectID: s.projectID, WorkspaceID: s.workspaceID, OldID: request.Supersedes,
			ExpectedRevision: request.ExpectedRevision,
			New: store.Record{ID: newID, Kind: "memo", SchemaVersion: schemaVersion, ProjectID: s.projectID,
				WorkspaceID: s.workspaceID, Revision: 1, Source: request.Source, WriterClass: "intention",
				Validity: "current", CreatedAt: now, UpdatedAt: now, Payload: payload,
				EvidenceRefs: nonNil(request.EvidenceRefs), Supersedes: request.Supersedes},
		})
		err = mutationStoreError(err)
	} else {
		record, err = s.mutate(ctx, "memo", schemaVersion, request.Source, request.Mode, request.ID, request.ExpectedRevision, payload, request.EvidenceRefs)
	}
	if request.Mode == "create" && request.TopicKey != "" && request.Supersedes == "" && errors.Is(err, store.ErrConflict) {
		if existing, ok, lookupErr := s.currentTopicMemo(ctx, request.Scope, request.Configuration, request.TopicKey); lookupErr == nil && ok {
			return s.existingTopicResponse(existing, request.ResponseView)
		}
	}
	response, err := s.mutationResponse(record, false, request.ResponseView, err)
	if err == nil {
		response.Warnings = warnings
	}
	return response, err
}

func (s *Service) existingTopicResponse(record store.Record, view string) (MutationResponse, error) {
	response, err := s.mutationResponse(record, false, view, nil)
	if err != nil {
		return MutationResponse{}, err
	}
	ref := "record:" + record.ID
	response.Status = contracts.StatusPartial
	response.Warnings = []contracts.Warning{{Code: "memo_topic_exists", Message: "this current topic already exists; update it by id/revision or supersede it before creating a replacement", Ref: &ref}}
	return response, nil
}

func (s *Service) currentTopicMemo(ctx context.Context, scope, configuration, topicKey string) (store.Record, bool, error) {
	page, err := s.records.QueryRecords(ctx, store.RecordQuery{
		ProjectID: s.projectID, WorkspaceID: s.workspaceID, Kind: "memo", Validity: "current",
		TopicKey: topicKey, Scope: scope, Configuration: configuration, Limit: 1,
	})
	if err != nil {
		return store.Record{}, false, err
	}
	if len(page.Records) == 1 {
		return page.Records[0], true, nil
	}
	return store.Record{}, false, nil
}

func (s *Service) relatedTopicWarnings(ctx context.Context, request MemoRequest) []contracts.Warning {
	page, err := s.records.QueryRecords(ctx, store.RecordQuery{
		ProjectID: s.projectID, WorkspaceID: s.workspaceID, Kind: "memo", Validity: "current",
		Terms: []string{request.Scope}, Limit: maximumRecordQuery,
	})
	if err != nil {
		return []contracts.Warning{{Code: "memo_topic_check_unknown", Message: "related memo topics could not be checked"}}
	}
	warnings := contracts.EmptyWarnings()
	for _, record := range page.Records {
		var payload struct {
			Scope         string `json:"scope"`
			Configuration string `json:"configuration"`
			TopicKey      string `json:"topic_key"`
		}
		if json.Unmarshal(record.Payload, &payload) != nil || payload.Scope != request.Scope || payload.Configuration != request.Configuration || payload.TopicKey == "" || payload.TopicKey == request.TopicKey {
			continue
		}
		ref := "record:" + record.ID
		warnings = append(warnings, contracts.Warning{Code: "memo_topic_related", Message: "related current memo topic: " + payload.TopicKey, Ref: &ref})
		if len(warnings) == 3 {
			break
		}
	}
	return warnings
}

func (s *Service) hostFactWarnings(ctx context.Context, request MemoRequest) []contracts.Warning {
	if request.MemoKind != "host_fact" || request.Host == nil || request.Mode == "supersede" {
		return contracts.EmptyWarnings()
	}
	page, err := s.records.QueryRecords(ctx, store.RecordQuery{
		ProjectID: s.projectID, WorkspaceID: s.workspaceID, Kind: "memo", Validity: "current",
		Terms: []string{request.Host.Alias}, Limit: maximumRecordQuery,
	})
	if err != nil {
		return []contracts.Warning{{Code: "host_fact_check_unknown", Message: "current host facts could not be checked"}}
	}
	warnings := contracts.EmptyWarnings()
	for _, record := range page.Records {
		if record.ID == request.ID {
			continue
		}
		var payload struct {
			MemoKind string    `json:"memo_kind"`
			Host     *HostFact `json:"host"`
		}
		if record.SchemaVersion != "memo.v2" || json.Unmarshal(record.Payload, &payload) != nil || payload.MemoKind != "host_fact" || payload.Host == nil || !strings.EqualFold(payload.Host.Alias, request.Host.Alias) {
			continue
		}
		if strings.EqualFold(payload.Host.OS, request.Host.OS) && strings.EqualFold(payload.Host.Role, request.Host.Role) {
			continue
		}
		ref := "record:" + record.ID
		warnings = append(warnings, contracts.Warning{Code: "host_fact_conflict", Message: "a current fact for this alias disagrees on os or role", Ref: &ref})
		if len(warnings) == 8 {
			break
		}
	}
	if !page.Complete && len(warnings) < 8 {
		warnings = append(warnings, contracts.Warning{Code: "host_fact_check_partial", Message: "host fact conflict check was bounded"})
	}
	return warnings
}

func (s *Service) ImportReport(ctx context.Context, request ImportRequest) (MutationResponse, error) {
	if request.ByteLimit == 0 {
		request.ByteLimit = maximumReportBytes
	}
	limits, err := contracts.NormalizeLimits(contracts.LimitRequest{ByteLimit: request.ByteLimit, TimeLimitMS: request.TimeLimitMS})
	if err != nil {
		return MutationResponse{}, err
	}
	if request.Path == "" || request.ChecklistPath == "" || request.Configuration == "" {
		return MutationResponse{}, errors.New("path, checklist_path, and configuration are required")
	}
	if err := validateResponseView(request.ResponseView); err != nil {
		return MutationResponse{}, err
	}
	if len(request.Path) > 4096 || len(request.ChecklistPath) > 4096 || len(request.Configuration) > 128 {
		return MutationResponse{}, contracts.ErrLimitExceeded
	}
	if limits.ByteLimit > maximumReportBytes {
		return MutationResponse{}, contracts.ErrLimitExceeded
	}
	ctx, cancel := context.WithTimeout(ctx, limits.TimeLimit)
	defer cancel()
	path, err := s.root.ResolvePrimaryExisting(filepath.FromSlash(request.Path))
	if err != nil {
		return MutationResponse{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return MutationResponse{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limits.ByteLimit)+1))
	if err != nil {
		return MutationResponse{}, err
	}
	if uint64(len(data)) > limits.ByteLimit {
		return MutationResponse{}, contracts.ErrLimitExceeded
	}
	report, summary, err := parseReport(data)
	if err != nil {
		return MutationResponse{}, err
	}
	checklist, checklistHash, err := s.readChecklist(ctx, request.ChecklistPath, request.Configuration)
	if err != nil {
		return MutationResponse{}, err
	}
	currentCommit, currentDirty, observed := currentSubject(ctx, s.root.Resolved())
	validity := "unknown"
	if observed && report.Subject.GitCommit != "" && report.Subject.Dirty != nil {
		if currentCommit == report.Subject.GitCommit && !currentDirty && !*report.Subject.Dirty {
			if !report.Truncated {
				validity = "current"
			}
		} else if currentCommit != report.Subject.GitCommit || (!*report.Subject.Dirty && currentDirty) {
			validity = "stale"
		}
	}
	hash := sha256.Sum256(data)
	sourceHash := "sha256:" + hex.EncodeToString(hash[:])
	outcome := verificationOutcome(report, checklist)
	payload, _ := json.Marshal(map[string]any{
		"operation": report.Operation, "status": report.Status, "subject": report.Subject,
		"counts": report.Counts, "checks": summary, "truncated": report.Truncated,
		"report_hash": sourceHash, "parser_revision": parserRevision, "raw_storage": "hash_only",
		"source_path": filepath.ToSlash(request.Path),
		"check_id":    checklist.ID, "checklist_revision": checklist.Revision, "checklist_hash": checklistHash,
		"configuration": request.Configuration, "applies_to": checklist.AppliesTo.Paths,
		"required": checklist.Required, "capability": checklist.Capability, "outcome": outcome,
	})
	if len(payload) > maximumRecordPayload {
		return MutationResponse{}, contracts.ErrLimitExceeded
	}
	identity := sha256.Sum256([]byte(sourceHash + "\x00" + checklistHash + "\x00" + request.Configuration))
	importIdentity := "sha256:" + hex.EncodeToString(identity[:])
	now := s.now().UTC()
	id, err := randomID("verification_")
	if err != nil {
		return MutationResponse{}, err
	}
	record, duplicate, err := s.records.ImportVerification(ctx, store.RecordCreate{Record: store.Record{
		ID: id, Kind: "verification", SchemaVersion: "verification-result.v1", ProjectID: s.projectID, WorkspaceID: s.workspaceID,
		Revision: 1, Source: "imported", WriterClass: "importer", Validity: validity, CreatedAt: now, UpdatedAt: now,
		Payload: payload, EvidenceRefs: []string{"report:" + sourceHash},
	}}, importIdentity, parserRevision)
	err = mutationStoreError(err)
	return s.mutationResponse(record, duplicate, request.ResponseView, err)
}

func (s *Service) mutate(ctx context.Context, kind, schemaVersion, source, mode, id string, expected uint64, payload json.RawMessage, evidence []string) (store.Record, error) {
	now := s.now().UTC()
	switch mode {
	case "create":
		newID, err := randomID(kind + "_")
		if err != nil {
			return store.Record{}, err
		}
		record := store.Record{ID: newID, Kind: kind, SchemaVersion: schemaVersion, ProjectID: s.projectID, WorkspaceID: s.workspaceID, Revision: 1, Source: source, WriterClass: "intention", Validity: "current", CreatedAt: now, UpdatedAt: now, Payload: payload, EvidenceRefs: nonNil(evidence)}
		if kind == "checkpoint" {
			created, err := s.records.CreateCheckpoint(ctx, store.RecordCreate{Record: record})
			return created, mutationStoreError(err)
		}
		created, err := s.records.CreateMemo(ctx, store.RecordCreate{Record: record})
		return created, mutationStoreError(err)
	case "update", "supersede":
		validity := "current"
		if mode == "supersede" {
			validity = "superseded"
		}
		update := store.RecordUpdate{ProjectID: s.projectID, WorkspaceID: s.workspaceID, ID: id, ExpectedRevision: expected, Payload: payload, EvidenceRefs: nonNil(evidence), Validity: validity}
		if mode == "supersede" {
			current, err := s.records.GetRecord(ctx, s.projectID, s.workspaceID, id)
			if err != nil {
				return store.Record{}, mutationStoreError(err)
			}
			if current.Kind != kind || current.Revision != expected {
				return store.Record{}, store.ErrConflict
			}
			update.Payload = current.Payload
			update.EvidenceRefs = current.EvidenceRefs
			update.Supersedes = current.Supersedes
		}
		if kind == "checkpoint" {
			updated, err := s.records.UpdateCheckpoint(ctx, update)
			return updated, mutationStoreError(err)
		}
		updated, err := s.records.UpdateMemo(ctx, update)
		return updated, mutationStoreError(err)
	default:
		return store.Record{}, fmt.Errorf("unsupported mutation mode %q", mode)
	}
}

func mutationStoreError(err error) error {
	if err == nil || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrStorageFailure, err)
}

func invalidArgument(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrInvalidArgument, err)
}

func (s *Service) mutationResponse(record store.Record, duplicate bool, view string, err error) (MutationResponse, error) {
	if err != nil {
		return MutationResponse{}, err
	}
	if view == "" || view == "receipt" {
		return MutationResponse{Status: contracts.StatusOK, Record: RecordResult{ID: record.ID, Revision: record.Revision},
			Duplicate: duplicate, Warnings: contracts.EmptyWarnings()}, nil
	}
	result, err := s.result(record, nil, nil, "full")
	if err != nil {
		return MutationResponse{}, err
	}
	return MutationResponse{Status: contracts.StatusOK, Record: result, Duplicate: duplicate, Warnings: contracts.EmptyWarnings()}, nil
}

func (s *Service) result(record store.Record, current *subjectObservation, payloadFields []string, view string) (RecordResult, error) {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(record.Payload))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return RecordResult{}, fmt.Errorf("decode record payload: %w", err)
	}
	validity := record.Validity
	if record.Kind == "verification" && validity != "superseded" && current != nil {
		validity = validityFromPayload(payload, *current)
	}
	if view == "brief" {
		return briefRecord(record, payload, validity), nil
	}
	complete := len(payloadFields) == 0 && view != "discovery"
	if view == "discovery" {
		payload = discoveryPayload(record.Kind, payload)
	} else if !complete {
		projected := make(map[string]any, len(payloadFields))
		for _, field := range payloadFields {
			if value, ok := payload[field]; ok {
				projected[field] = value
			}
		}
		payload = projected
	}
	return RecordResult{ID: record.ID, Kind: record.Kind, SchemaVersion: record.SchemaVersion, Revision: record.Revision, Source: record.Source, WriterClass: record.WriterClass, Validity: validity, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt, Payload: payload, PayloadComplete: complete, EvidenceRefs: nonNil(record.EvidenceRefs), Supersedes: record.Supersedes}, nil
}

func resumeRecord(record store.Record) (RecordResult, error) {
	var payload struct {
		Goal            string   `json:"goal"`
		NextAction      string   `json:"next_action"`
		ChangeSummary   string   `json:"change_summary"`
		BackgroundRefs  []string `json:"background_refs"`
		RemainingChecks []string `json:"remaining_checks"`
	}
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		return RecordResult{}, fmt.Errorf("decode checkpoint: %w", err)
	}
	state := "unknown"
	if strings.TrimSpace(payload.ChangeSummary) != "" {
		state = "recorded"
	}
	checks := make([]string, 0, min(len(payload.RemainingChecks), 5))
	for _, check := range payload.RemainingChecks[:min(len(payload.RemainingChecks), 5)] {
		checks = append(checks, compactPreview(check, 160))
	}
	refs := append([]string(nil), record.EvidenceRefs[:min(len(record.EvidenceRefs), 5)]...)
	background := append([]string(nil), payload.BackgroundRefs[:min(len(payload.BackgroundRefs), 5)]...)
	return RecordResult{ID: record.ID, Kind: record.Kind, Revision: record.Revision, Validity: record.Validity,
		UpdatedAt: record.UpdatedAt, Title: compactPreview(payload.Goal, 120),
		NextAction: compactPreview(payload.NextAction, 320), ChangeSummary: compactPreview(payload.ChangeSummary, 240),
		ChangeState: state, RemainingChecks: checks, EvidenceRefs: refs, BackgroundRefs: background}, nil
}

func briefRecord(record store.Record, payload map[string]any, validity string) RecordResult {
	field := func(name string) string {
		value, _ := payload[name].(string)
		return strings.TrimSpace(value)
	}
	title := field("title")
	summary := field("summary")
	if record.Kind == "memo" {
		if host, ok := payload["host"].(map[string]any); ok {
			if alias, ok := host["alias"].(string); ok && title == "" {
				title = alias
			}
			if summary == "" {
				role, _ := host["role"].(string)
				osName, _ := host["os"].(string)
				summary = strings.TrimSpace(role + " " + osName)
			}
		}
		if title == "" {
			title = field("topic_key")
			if title == "" {
				title = field("scope")
			}
		}
		if summary == "" {
			summary = field("content")
		}
	} else if record.Kind == "checkpoint" {
		if title == "" {
			title = field("goal")
		}
		if summary == "" {
			summary = field("next_action")
		}
	} else {
		if title == "" {
			for _, name := range []string{"capability_id", "check_id", "path"} {
				if title = field(name); title != "" {
					break
				}
			}
		}
		if summary == "" {
			for _, name := range []string{"summary", "status", "state", "outcome"} {
				if summary = field(name); summary != "" {
					break
				}
			}
		}
	}
	if title == "" {
		title = record.Kind
	}
	return RecordResult{ID: record.ID, Kind: record.Kind, Validity: validity,
		UpdatedAt: record.UpdatedAt, Title: compactPreview(title, 96), Summary: compactPreview(summary, 140),
		TopicKey: field("topic_key"), Scope: compactPreview(field("scope"), 96),
		Configuration: compactPreview(field("configuration"), 96), TemporalKind: briefTemporalKind(field("temporal_kind")),
		AsOf: field("as_of"), Source: record.Source, PayloadComplete: false}
}

func briefTemporalKind(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func validateFilters(request QueryRequest) error {
	if request.Kind != "" && request.Kind != "verification" && request.Kind != "checkpoint" && request.Kind != "memo" && request.Kind != "environment" && request.Kind != "run" && request.Kind != "artifact" {
		return errors.New("invalid record kind")
	}
	if request.Validity != "" && request.Validity != "current" && request.Validity != "stale" && request.Validity != "unknown" && request.Validity != "superseded" {
		return errors.New("invalid record validity")
	}
	if request.Source != "" && request.Source != "observed" && request.Source != "imported" && request.Source != "user_asserted" && request.Source != "llm_proposed" {
		return errors.New("invalid record source")
	}
	if request.MatchMode != "" && request.MatchMode != "any" && request.MatchMode != "all" {
		return errors.New("match_mode must be any or all")
	}
	if request.ResponseView != "" && request.ResponseView != "brief" && request.ResponseView != "discovery" && request.ResponseView != "full" {
		return errors.New("response_view must be brief, discovery, or full")
	}
	if request.TopicKey != "" && !topicKeyPattern.MatchString(strings.ToLower(request.TopicKey)) {
		return errors.New("topic_key must use lowercase letters, digits, dot, underscore, slash, or hyphen")
	}
	if len(request.Scope) > 4096 || len(request.Configuration) > 4096 {
		return contracts.ErrLimitExceeded
	}
	if request.Mode == "search" && strings.TrimSpace(request.Query) == "" {
		return errors.New("query is required for search")
	}
	if request.Mode != "search" && request.Mode != "resume" && request.Query != "" {
		return errors.New("query is only valid for search or resume")
	}
	if request.Mode == "get_topic" && request.TopicKey == "" {
		return errors.New("topic_key is required for get_topic")
	}
	if request.Mode == "get_topic" && request.Kind != "" && request.Kind != "memo" {
		return errors.New("kind must be memo for get_topic")
	}
	if (request.ResponseView == "brief" || request.ResponseView == "discovery") && len(request.PayloadFields) > 0 {
		return errors.New("payload_fields require response_view full")
	}
	terms := normalizeRecordTerms(request.Query)
	if len(request.Query) > 512 || len(terms) > 16 {
		return contracts.ErrLimitExceeded
	}
	for _, term := range terms {
		if len(term) > 128 {
			return contracts.ErrLimitExceeded
		}
	}
	if err := validatePayloadFields(request.PayloadFields); err != nil {
		return err
	}
	return nil
}

func normalizeRecordTerms(query string) []string {
	seen := map[string]struct{}{}
	terms := make([]string, 0)
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if _, exists := seen[term]; exists {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	return terms
}

func discoveryPayload(kind string, payload map[string]any) map[string]any {
	fields := map[string][]string{
		"memo":         {"memo_kind", "scope", "configuration", "topic_key"},
		"checkpoint":   {"goal", "next_action", "remaining_checks"},
		"verification": {"check_id", "status", "outcome", "configuration", "truncated"},
		"environment":  {"capability_id", "configuration", "ready", "observed_at"},
		"run":          {"capability_id", "configuration", "state", "started_at", "finished_at"},
		"artifact":     {"path", "storage", "size", "content_hash"},
	}
	selected := make(map[string]any)
	for _, field := range fields[kind] {
		if value, ok := payload[field]; ok {
			selected[field] = compactDiscoveryValue(value)
		}
	}
	if kind == "memo" {
		if content, ok := payload["content"].(string); ok {
			selected["preview"] = compactPreview(content, 320)
		}
		if host, ok := payload["host"].(map[string]any); ok {
			compact := make(map[string]any, len(host))
			for _, field := range []string{"alias", "role", "os", "tier", "services", "paths", "confirmed_at"} {
				if value, exists := host[field]; exists {
					compact[field] = compactDiscoveryValue(value)
				}
			}
			selected["host"] = compact
		}
	}
	return selected
}

func compactDiscoveryValue(value any) any {
	switch typed := value.(type) {
	case string:
		return compactPreview(typed, 320)
	case []any:
		limit := len(typed)
		if limit > 8 {
			limit = 8
		}
		compact := make([]any, 0, limit)
		for _, item := range typed[:limit] {
			compact = append(compact, compactDiscoveryValue(item))
		}
		return compact
	default:
		return value
	}
}

func compactPreview(value string, maximumRunes int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= maximumRunes {
		return value
	}
	return string(runes[:maximumRunes-1]) + "…"
}

func validateCheckpoint(request CheckpointRequest) error {
	if err := validateResponseView(request.ResponseView); err != nil {
		return err
	}
	if request.Mode != "create" && request.Mode != "update" && request.Mode != "supersede" {
		return errors.New("mode must be create, update, or supersede")
	}
	if request.Mode == "create" && strings.TrimSpace(request.Goal) == "" {
		return errors.New("goal is required for create")
	}
	if request.Mode != "create" && (request.ID == "" || request.ExpectedRevision == 0) {
		return errors.New("id and expected_revision are required for update or supersede")
	}
	return validateTextAndRefs(request.Goal+request.BaselineCommit+request.NextAction+request.ChangeSummary, append(append(append(request.RunRefs, request.RemainingChecks...), request.BackgroundRefs...), append(request.Risks, request.EvidenceRefs...)...))
}

func validateMemo(request MemoRequest) error {
	if err := validateResponseView(request.ResponseView); err != nil {
		return err
	}
	if request.Mode != "create" && request.Mode != "update" && request.Mode != "supersede" {
		return errors.New("mode must be create, update, or supersede")
	}
	allowedKind := request.MemoKind == "decision" || request.MemoKind == "failed_attempt" || request.MemoKind == "resolved_failure" || request.MemoKind == "limitation" || request.MemoKind == "host_fact"
	if request.Mode != "supersede" && (!allowedKind || (request.MemoKind != "host_fact" && strings.TrimSpace(request.Content) == "")) {
		return errors.New("memo_kind and content are required")
	}
	if request.Mode != "supersede" {
		if request.MemoKind == "host_fact" {
			if request.TopicKey != "" {
				return &ValidationError{Field: "topic_key", Reason: "is not valid for host_fact"}
			}
			if request.Title != "" || request.Summary != "" || request.TemporalKind != "" || request.AsOf != "" {
				return &ValidationError{Field: "title/summary/temporal_kind/as_of", Reason: "are not valid for host_fact"}
			}
			if err := validateHostFact(request.Host, request.InvalidationCondition); err != nil {
				return err
			}
		} else if request.Host != nil {
			return errors.New("host is only valid for host_fact")
		}
	}
	if request.TopicKey != "" && (strings.TrimSpace(request.Scope) == "" || !topicKeyPattern.MatchString(request.TopicKey)) {
		return errors.New("topic_key requires scope and must use lowercase letters, digits, dot, underscore, slash, or hyphen")
	}
	if request.Supersedes != "" && (request.Mode != "create" || request.ExpectedRevision == 0 || len(request.Supersedes) > 128) {
		return errors.New("supersedes requires create mode and expected_revision")
	}
	if len(request.Title) > 160 || len(request.Summary) > 240 {
		return contracts.ErrLimitExceeded
	}
	if request.TemporalKind != "" && request.TemporalKind != "historical_observation" && request.TemporalKind != "current_guidance" {
		return errors.New("temporal_kind must be historical_observation or current_guidance")
	}
	if (request.TemporalKind == "") != (request.AsOf == "") {
		return errors.New("temporal_kind and as_of must be provided together")
	}
	if request.AsOf != "" {
		if _, err := time.Parse(time.RFC3339, request.AsOf); err != nil {
			return errors.New("as_of must be RFC3339")
		}
	}
	if request.Source != "user_asserted" && request.Source != "llm_proposed" {
		return errors.New("source must be user_asserted or llm_proposed")
	}
	if request.Mode != "create" && (request.ID == "" || request.ExpectedRevision == 0) {
		return errors.New("id and expected_revision are required for update or supersede")
	}
	return validateTextAndRefs(request.MemoKind+request.Scope+request.Configuration+request.Title+request.Summary+request.Content+request.InvalidationCondition, request.EvidenceRefs)
}

func validateHostFact(host *HostFact, invalidation string) error {
	if host == nil {
		return &ValidationError{Field: "host", Reason: "is required for host_fact"}
	}
	if !hostAliasPattern.MatchString(host.Alias) {
		return &ValidationError{Field: "host.alias", Reason: "must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$"}
	}
	if strings.TrimSpace(host.Role) == "" {
		return &ValidationError{Field: "host.role", Reason: "is required"}
	}
	if strings.TrimSpace(host.OS) == "" {
		return &ValidationError{Field: "host.os", Reason: "is required"}
	}
	if strings.TrimSpace(invalidation) == "" {
		return &ValidationError{Field: "invalidation_condition", Reason: "is required for host_fact"}
	}
	if strings.TrimSpace(host.Role) != host.Role || strings.TrimSpace(host.OS) != host.OS {
		return &ValidationError{Field: "host.role/host.os", Reason: "must not have surrounding whitespace"}
	}
	if host.Tier != "production" && host.Tier != "test" && host.Tier != "staging" && host.Tier != "development" {
		return &ValidationError{Field: "host.tier", Reason: "must be production, test, staging, or development"}
	}
	if _, err := time.Parse(time.RFC3339, host.ConfirmedAt); err != nil {
		return &ValidationError{Field: "host.confirmed_at", Reason: "must be RFC3339"}
	}
	if len(host.Role) > 128 {
		return &ValidationError{Field: "host.role", Reason: "must contain at most 128 bytes"}
	}
	if len(host.OS) > 128 {
		return &ValidationError{Field: "host.os", Reason: "must contain at most 128 bytes"}
	}
	if err := validateStringList(append(append([]string(nil), host.Services...), host.Paths...), 64, 1024); err != nil {
		return &ValidationError{Field: "host.services/host.paths", Reason: "must contain at most 64 unique, trimmed entries of at most 1024 bytes combined"}
	}
	return nil
}

func validateStringList(values []string, maximumItems, maximumLength int) error {
	if len(values) > maximumItems {
		return contracts.ErrLimitExceeded
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != value || value == "" || len(value) > maximumLength {
			return errors.New("host services and paths must be bounded non-empty strings")
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			return errors.New("host services and paths must not contain duplicates")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateResponseView(view string) error {
	if view != "" && view != "full" && view != "receipt" {
		return errors.New("response_view must be full or receipt")
	}
	return nil
}

func validatePayloadFields(fields []string) error {
	if len(fields) > 32 {
		return contracts.ErrLimitExceeded
	}
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if field == "" || len(field) > 128 || strings.TrimSpace(field) != field {
			return errors.New("payload_fields must contain non-empty top-level field names")
		}
		if _, exists := seen[field]; exists {
			return errors.New("payload_fields must not contain duplicates")
		}
		seen[field] = struct{}{}
	}
	return nil
}

func validateTextAndRefs(text string, refs []string) error {
	if len(text) > 32*1024 || len(refs) > 128 {
		return contracts.ErrLimitExceeded
	}
	for _, ref := range refs {
		if len(ref) > 1024 {
			return contracts.ErrLimitExceeded
		}
	}
	return nil
}

type checkReport struct {
	SchemaVersion string    `json:"schema_version"`
	Operation     string    `json:"operation"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	Subject       struct {
		GitCommit string `json:"git_commit"`
		Dirty     *bool  `json:"dirty"`
	} `json:"subject"`
	Checks []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"checks"`
	Counts    map[string]int `json:"counts"`
	Truncated bool           `json:"truncated"`
}

func parseReport(data []byte) (checkReport, []map[string]string, error) {
	var report checkReport
	if err := json.Unmarshal(data, &report); err != nil {
		return report, nil, fmt.Errorf("%w: malformed JSON", ErrReportInvalid)
	}
	if report.SchemaVersion != "check-report.v1" || len(report.Checks) > 256 || (report.Status != "passed" && report.Status != "failed" && report.Status != "partial") {
		return report, nil, ErrReportInvalid
	}
	if report.Operation != "verify" && report.Operation != "preflight" && report.Operation != "public-release-check" {
		return report, nil, ErrReportInvalid
	}
	if report.StartedAt.IsZero() || report.FinishedAt.IsZero() || report.FinishedAt.Before(report.StartedAt) {
		return report, nil, ErrReportInvalid
	}
	if report.Subject.GitCommit != "" && !gitCommitPattern.MatchString(report.Subject.GitCommit) {
		return report, nil, ErrReportInvalid
	}
	summary := make([]map[string]string, 0, len(report.Checks))
	actual := map[string]int{"passed": 0, "failed": 0, "unknown": 0}
	for _, check := range report.Checks {
		if check.ID == "" || len(check.ID) > 256 || (check.Status != "passed" && check.Status != "failed" && check.Status != "unknown") {
			return report, nil, ErrReportInvalid
		}
		summary = append(summary, map[string]string{"id": check.ID, "status": check.Status})
		actual[check.Status]++
	}
	if len(report.Counts) != 3 {
		return report, nil, ErrReportInvalid
	}
	for _, status := range []string{"passed", "failed", "unknown"} {
		if report.Counts[status] != actual[status] || report.Counts[status] < 0 {
			return report, nil, ErrReportInvalid
		}
	}
	if report.Status == "passed" && (len(report.Checks) == 0 || actual["failed"] != 0 || actual["unknown"] != 0 || report.Truncated) {
		return report, nil, ErrReportInvalid
	}
	if report.Status == "failed" && actual["failed"] == 0 {
		return report, nil, ErrReportInvalid
	}
	if report.Status == "partial" && actual["unknown"] == 0 && !report.Truncated {
		return report, nil, ErrReportInvalid
	}
	return report, summary, nil
}

type subjectObservation struct {
	Commit   string
	Dirty    bool
	Observed bool
}

func (s *Service) observeSubject(ctx context.Context) subjectObservation {
	commit, dirty, observed := currentSubject(ctx, s.root.Resolved())
	return subjectObservation{Commit: commit, Dirty: dirty, Observed: observed}
}

// A Git evidence ref is a basis for review, not proof that memo content still
// describes the current source. Keep this check on full memo reads so brief
// discovery and Git-free workspaces stay cheap.
func (s *Service) memoBasisWarnings(ctx context.Context, record store.Record) []contracts.Warning {
	var basis string
	for _, ref := range record.EvidenceRefs {
		candidate := strings.TrimPrefix(ref, "git:")
		if candidate != ref && gitCommitPattern.MatchString(candidate) {
			basis = candidate
			break
		}
	}
	if basis == "" {
		return nil
	}
	ref := "git:" + basis
	current := s.observeSubject(ctx)
	switch {
	case !current.Observed:
		return []contracts.Warning{{Code: "memo_basis_unknown", Message: "the cited Git basis could not be compared with this workspace; verify current source before applying the memo", Ref: &ref}}
	case current.Commit != basis:
		return []contracts.Warning{{Code: "memo_basis_older", Message: "the workspace is at a different commit than this memo's cited basis; verify relevant source before applying it", Ref: &ref}}
	case current.Dirty:
		return []contracts.Warning{{Code: "memo_basis_dirty", Message: "the cited commit matches, but the worktree has uncommitted changes; verify relevant source before applying the memo", Ref: &ref}}
	default:
		return nil
	}
}

func validityFromPayload(payload map[string]any, current subjectObservation) string {
	subject, ok := payload["subject"].(map[string]any)
	if !ok {
		return "unknown"
	}
	commit, _ := subject["git_commit"].(string)
	dirty, ok := subject["dirty"].(bool)
	if !ok || commit == "" || !current.Observed {
		return "unknown"
	}
	if commit == current.Commit && !dirty && !current.Dirty {
		if truncated, _ := payload["truncated"].(bool); truncated {
			return "unknown"
		}
		return "current"
	}
	if commit != current.Commit || (!dirty && current.Dirty) {
		return "stale"
	}
	return "unknown"
}

type verificationChecklist struct {
	SchemaVersion string `json:"schema_version" yaml:"schema_version"`
	ID            string `json:"id" yaml:"id"`
	Revision      uint64 `json:"revision" yaml:"revision"`
	AppliesTo     struct {
		Paths []string `json:"paths" yaml:"paths"`
	} `json:"applies_to" yaml:"applies_to"`
	Required       bool     `json:"required" yaml:"required"`
	Capability     string   `json:"capability" yaml:"capability"`
	Configurations []string `json:"configurations" yaml:"configurations"`
	Success        struct {
		ReportRequired        bool   `json:"report_required" yaml:"report_required"`
		MinimumExecutedChecks uint64 `json:"minimum_executed_checks" yaml:"minimum_executed_checks"`
	} `json:"success" yaml:"success"`
}

func (s *Service) readChecklist(ctx context.Context, relative, configuration string) (verificationChecklist, string, error) {
	path, err := s.root.ResolvePrimaryExisting(filepath.FromSlash(relative))
	if err != nil {
		return verificationChecklist{}, "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return verificationChecklist{}, "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return verificationChecklist{}, "", contracts.ErrLimitExceeded
	}
	var checklist verificationChecklist
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&checklist)
		if err == nil {
			var extra any
			if extraErr := decoder.Decode(&extra); !errors.Is(extraErr, io.EOF) {
				err = errors.New("multiple JSON values")
			}
		}
	case ".yaml", ".yml":
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		err = decoder.Decode(&checklist)
		if err == nil {
			var extra any
			if extraErr := decoder.Decode(&extra); !errors.Is(extraErr, io.EOF) {
				err = errors.New("multiple YAML documents")
			}
		}
	default:
		return checklist, "", errors.New("unsupported checklist format")
	}
	if err != nil || checklist.SchemaVersion != "verification-check.v1" || !checklistIDPattern.MatchString(checklist.ID) || checklist.Revision == 0 || checklist.Capability == "" || len(checklist.AppliesTo.Paths) == 0 || len(checklist.Configurations) == 0 || checklist.Success.MinimumExecutedChecks == 0 {
		return checklist, "", errors.New("invalid verification checklist")
	}
	if len(checklist.ID) > 128 || len(checklist.Capability) > 128 || len(checklist.AppliesTo.Paths) > 128 || len(checklist.Configurations) > 64 {
		return checklist, "", contracts.ErrLimitExceeded
	}
	for _, value := range append(append([]string(nil), checklist.AppliesTo.Paths...), checklist.Configurations...) {
		if value == "" || len(value) > 1024 {
			return checklist, "", errors.New("invalid verification checklist value")
		}
	}
	configured := false
	for _, value := range checklist.Configurations {
		if value == configuration {
			configured = true
		}
	}
	if !configured {
		return checklist, "", errors.New("configuration is not declared by checklist")
	}
	if tracked, applicable := gitTracked(ctx, s.root.Resolved(), relative); applicable && !tracked {
		return checklist, "", errors.New("verification checklist must be tracked")
	}
	hash := sha256.Sum256(data)
	return checklist, "sha256:" + hex.EncodeToString(hash[:]), nil
}

func gitTracked(ctx context.Context, root, relative string) (tracked bool, applicable bool) {
	if err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--is-inside-work-tree").Run(); err != nil {
		return false, false
	}
	command := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--error-unmatch", "--", filepath.ToSlash(relative))
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run() == nil, true
}

func verificationOutcome(report checkReport, checklist verificationChecklist) string {
	executed := report.Counts["passed"] + report.Counts["failed"]
	if report.Truncated || report.Counts["unknown"] > 0 {
		return "unknown"
	}
	if report.Counts["failed"] > 0 || report.Status == "failed" {
		return "failed"
	}
	if uint64(executed) < checklist.Success.MinimumExecutedChecks || (checklist.Success.ReportRequired && len(report.Checks) == 0) {
		return "not_run"
	}
	if report.Status == "passed" {
		return "passed"
	}
	return "unknown"
}

type presenceWriter struct{ present bool }

func (w *presenceWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.present = true
	}
	return len(p), nil
}

func currentSubject(ctx context.Context, root string) (string, bool, bool) {
	commitOutput, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", false, false
	}
	dirty := false
	for _, args := range [][]string{{"-C", root, "diff", "--quiet", "--"}, {"-C", root, "diff", "--cached", "--quiet", "--"}} {
		err := exec.CommandContext(ctx, "git", args...).Run()
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			dirty = true
		} else if err != nil {
			return "", false, false
		}
	}
	writer := &presenceWriter{}
	command := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--others", "--exclude-standard", "--directory")
	command.Stdout = writer
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return "", false, false
	}
	return strings.TrimSpace(string(commitOutput)), dirty || writer.present, true
}

func (s *Service) persist(ctx context.Context, request QueryRequest, results []RecordResult, complete bool, itemLimit, byteLimit uint64) (QueryResponse, error) {
	now := s.now().UTC()
	id, err := randomID("record_results_")
	if err != nil {
		return QueryResponse{}, err
	}
	queryJSON, _ := json.Marshal(request)
	hash := sha256.Sum256(queryJSON)
	metadata, _ := json.Marshal(map[string]any{"complete": complete})
	set := store.ResultSet{ID: id, WorkspaceID: s.workspaceID, QueryHash: "sha256:" + hex.EncodeToString(hash[:]), GenerationID: "records.v1", CreatedAt: now, ExpiresAt: now.Add(resultSetTTL), Metadata: metadata, Items: make([]store.ResultItem, 0, len(results))}
	for index, result := range results {
		payload, _ := json.Marshal(result)
		sum := sha256.Sum256(payload)
		set.Items = append(set.Items, store.ResultItem{Ordinal: uint64(index), ItemRef: "record:" + result.ID, ItemHash: "sha256:" + hex.EncodeToString(sum[:]), Payload: payload})
	}
	if err := s.results.CreateResultSet(ctx, set); err != nil {
		return QueryResponse{}, err
	}
	return s.readPage(ctx, id, 0, itemLimit, byteLimit, set.ExpiresAt)
}

type persistedMetadata struct {
	Complete bool `json:"complete"`
}

func (s *Service) fromCursor(ctx context.Context, encoded string, itemLimit, byteLimit uint64) (QueryResponse, error) {
	payload, err := s.cursors.Decode(encoded, s.workspaceID)
	if err != nil {
		return QueryResponse{}, err
	}
	return s.readPage(ctx, payload.ResultSetID, payload.NextOrdinal, itemLimit, byteLimit, time.Unix(payload.ExpiresUnix, 0))
}

func (s *Service) readPage(ctx context.Context, setID string, from, itemLimit, byteLimit uint64, expires time.Time) (QueryResponse, error) {
	page, err := s.results.ReadResultPage(ctx, setID, from, itemLimit)
	if err != nil {
		return QueryResponse{}, err
	}
	items := make([]RecordResult, 0, len(page.Items))
	metadata := persistedMetadata{Complete: true}
	if len(page.Metadata) > 0 {
		if err := json.Unmarshal(page.Metadata, &metadata); err != nil {
			return QueryResponse{}, fmt.Errorf("decode record result metadata: %w", err)
		}
	}
	for _, stored := range page.Items {
		var item RecordResult
		if err := json.Unmarshal(stored.Payload, &item); err != nil {
			return QueryResponse{}, err
		}
		items = append(items, item)
	}
	for returned := len(items); returned >= 0; returned-- {
		next := from + uint64(returned)
		more := next < page.Total
		var nextCursor *string
		if more {
			encoded, err := s.cursors.Encode(s.workspaceID, setID, next, expires)
			if err != nil {
				return QueryResponse{}, err
			}
			nextCursor = &encoded
		}
		matched := page.Total
		snapshot := "snapshot:" + setID
		truncated := more || !metadata.Complete
		status := contracts.StatusOK
		relation := contracts.CountExact
		scanState := contracts.ScanComplete
		warnings := contracts.EmptyWarnings()
		if !metadata.Complete {
			status = contracts.StatusPartial
			relation = contracts.CountLowerBound
			scanState = contracts.ScanPartial
			warnings = append(warnings, contracts.Warning{Code: "record_snapshot_limit", Message: "record snapshot exceeded its bounded item limit"})
		}
		response := QueryResponse{Status: status, Items: items[:returned], Counts: contracts.Counts{Matched: &matched, Relation: relation, Returned: uint64(returned)}, Scan: contracts.Scan{State: scanState}, Truncated: &truncated, NextCursor: nextCursor, SnapshotRef: &snapshot, Warnings: warnings}
		encoded, _ := json.Marshal(response)
		if uint64(len(encoded)) <= byteLimit {
			if returned == 0 && len(items) > 0 {
				return QueryResponse{}, ErrResponseTooLarge
			}
			return response, nil
		}
	}
	return QueryResponse{}, ErrResponseTooLarge
}

func singleResponse(item RecordResult, byteLimit uint64) (QueryResponse, error) {
	one := uint64(1)
	truncated := false
	response := QueryResponse{Status: contracts.StatusOK, Items: []RecordResult{item}, Counts: contracts.Counts{Matched: &one, Relation: contracts.CountExact, Returned: 1}, Scan: contracts.Scan{State: contracts.ScanNotApplicable}, Truncated: &truncated, Warnings: contracts.EmptyWarnings()}
	encoded, _ := json.Marshal(response)
	if uint64(len(encoded)) > byteLimit {
		return QueryResponse{}, ErrResponseTooLarge
	}
	return response, nil
}

func ambiguousResponse(items []RecordResult, matched uint64, code, message string, byteLimit uint64) (QueryResponse, error) {
	for returned := len(items); returned > 0; returned-- {
		truncated := matched > uint64(returned)
		response := QueryResponse{Status: contracts.StatusPartial, Items: items[:returned],
			Counts: contracts.Counts{Matched: &matched, Relation: contracts.CountExact, Returned: uint64(returned)},
			Scan:   contracts.Scan{State: contracts.ScanComplete}, Truncated: &truncated,
			Warnings: []contracts.Warning{{Code: code, Message: message}}}
		encoded, _ := json.Marshal(response)
		if uint64(len(encoded)) <= byteLimit {
			return response, nil
		}
	}
	return QueryResponse{}, ErrResponseTooLarge
}

func randomID(prefix string) (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(data), nil
}
func nonNil[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}
