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
	ErrResponseTooLarge = errors.New("records: response cannot fit byte_limit")
	ErrPermissionDenied = errors.New("records: permission denied")
	ErrReportInvalid    = errors.New("records: report invalid")
	checklistIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	gitCommitPattern    = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

type QueryRequest struct {
	Mode         string `json:"mode,omitempty" jsonschema:"record operation: search, list, or get; omit when using cursor"`
	ID           string `json:"id,omitempty" jsonschema:"opaque record ID; required for get"`
	Kind         string `json:"kind,omitempty" jsonschema:"record kind filter: verification, checkpoint, memo, environment, run, or artifact"`
	Validity     string `json:"validity,omitempty" jsonschema:"validity filter: current, stale, unknown, or superseded"`
	Source       string `json:"source,omitempty" jsonschema:"source filter: observed, imported, user_asserted, or llm_proposed"`
	UpdatedAfter string `json:"updated_after,omitempty" jsonschema:"RFC3339 lower bound for record update time"`
	Cursor       string `json:"cursor,omitempty" jsonschema:"opaque cursor from an earlier project_records query"`
	ItemLimit    uint64 `json:"item_limit,omitempty" jsonschema:"maximum returned items; default 50, maximum 500"`
	ByteLimit    uint64 `json:"byte_limit,omitempty" jsonschema:"maximum response bytes; default 65536, maximum 1048576"`
	TimeLimitMS  int64  `json:"time_limit_ms,omitempty" jsonschema:"operation deadline in milliseconds; default 5000, maximum 30000"`
}

type RecordResult struct {
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	SchemaVersion string         `json:"schema_version"`
	Revision      uint64         `json:"revision"`
	Source        string         `json:"source"`
	WriterClass   string         `json:"writer_class"`
	Validity      string         `json:"validity"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	Payload       map[string]any `json:"payload"`
	EvidenceRefs  []string       `json:"evidence_refs"`
	Supersedes    string         `json:"supersedes,omitempty"`
}

type QueryResponse = contracts.Response[RecordResult]

type CheckpointRequest struct {
	Mode             string   `json:"mode" jsonschema:"checkpoint mutation: create, update, or supersede"`
	ID               string   `json:"id,omitempty" jsonschema:"record ID; required for update or supersede"`
	ExpectedRevision uint64   `json:"expected_revision,omitempty" jsonschema:"current revision; required for update or supersede"`
	Goal             string   `json:"goal,omitempty" jsonschema:"short task objective"`
	BaselineCommit   string   `json:"baseline_commit,omitempty" jsonschema:"observed baseline Git commit"`
	Dirty            *bool    `json:"dirty,omitempty" jsonschema:"whether the observed baseline worktree was dirty"`
	RunRefs          []string `json:"run_refs,omitempty" jsonschema:"related run or report refs"`
	RemainingChecks  []string `json:"remaining_checks,omitempty" jsonschema:"checks still required"`
	NextAction       string   `json:"next_action,omitempty" jsonschema:"next intended action"`
	Risks            []string `json:"risks,omitempty" jsonschema:"known unverified risks"`
	EvidenceRefs     []string `json:"evidence_refs,omitempty" jsonschema:"bounded evidence refs"`
}

type MemoRequest struct {
	Mode                  string   `json:"mode" jsonschema:"memo mutation: create, update, or supersede"`
	ID                    string   `json:"id,omitempty" jsonschema:"record ID; required for update or supersede"`
	ExpectedRevision      uint64   `json:"expected_revision,omitempty" jsonschema:"current revision; required for update or supersede"`
	MemoKind              string   `json:"memo_kind,omitempty" jsonschema:"decision, failed_attempt, resolved_failure, or limitation"`
	Scope                 string   `json:"scope,omitempty" jsonschema:"workspace-relative logical scope"`
	Configuration         string   `json:"configuration,omitempty" jsonschema:"applicable configuration"`
	Content               string   `json:"content,omitempty" jsonschema:"bounded memo content"`
	InvalidationCondition string   `json:"invalidation_condition,omitempty" jsonschema:"condition that makes the memo stale"`
	Source                string   `json:"source,omitempty" jsonschema:"user_asserted or llm_proposed"`
	EvidenceRefs          []string `json:"evidence_refs,omitempty" jsonschema:"bounded evidence refs"`
}

type ImportRequest struct {
	Path          string `json:"path" jsonschema:"workspace-relative check-report.v1 path"`
	ChecklistPath string `json:"checklist_path" jsonschema:"workspace-relative verification-check.v1 YAML or JSON path"`
	Configuration string `json:"configuration" jsonschema:"declared checklist configuration being imported"`
	ByteLimit     uint64 `json:"byte_limit,omitempty" jsonschema:"maximum report bytes; default and maximum 1048576"`
	TimeLimitMS   int64  `json:"time_limit_ms,omitempty" jsonschema:"operation deadline in milliseconds; default 5000, maximum 30000"`
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
	current := s.observeSubject(ctx)
	switch request.Mode {
	case "get":
		if request.ID == "" {
			return QueryResponse{}, errors.New("id is required for get")
		}
		record, err := s.records.GetRecord(ctx, s.projectID, s.workspaceID, request.ID)
		if err != nil {
			return QueryResponse{}, err
		}
		result, err := s.result(record, &current)
		if err != nil {
			return QueryResponse{}, err
		}
		return singleResponse(result, limits.ByteLimit)
	case "list", "search":
		var updatedAfter time.Time
		if request.UpdatedAfter != "" {
			updatedAfter, err = time.Parse(time.RFC3339, request.UpdatedAfter)
			if err != nil {
				return QueryResponse{}, errors.New("updated_after must be RFC3339")
			}
		}
		page, err := s.records.QueryRecords(ctx, store.RecordQuery{ProjectID: s.projectID, WorkspaceID: s.workspaceID, Kind: request.Kind, Source: request.Source, UpdatedAfter: updatedAfter, Limit: maximumRecordQuery})
		if err != nil {
			return QueryResponse{}, err
		}
		items := make([]RecordResult, 0, len(page.Records))
		for _, record := range page.Records {
			result, err := s.result(record, &current)
			if err != nil {
				return QueryResponse{}, err
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
		return MutationResponse{}, err
	}
	payload, _ := json.Marshal(map[string]any{"goal": request.Goal, "baseline_commit": request.BaselineCommit, "dirty": request.Dirty, "run_refs": nonNil(request.RunRefs), "remaining_checks": nonNil(request.RemainingChecks), "next_action": request.NextAction, "risks": nonNil(request.Risks)})
	if len(payload) > maximumRecordPayload {
		return MutationResponse{}, contracts.ErrLimitExceeded
	}
	record, err := s.mutate(ctx, "checkpoint", "checkpoint.v1", "user_asserted", request.Mode, request.ID, request.ExpectedRevision, payload, request.EvidenceRefs)
	return s.mutationResponse(record, false, err)
}

func (s *Service) WriteMemo(ctx context.Context, request MemoRequest) (MutationResponse, error) {
	if request.Source == "" {
		request.Source = "user_asserted"
	}
	if err := validateMemo(request); err != nil {
		return MutationResponse{}, err
	}
	payload, _ := json.Marshal(map[string]any{"memo_kind": request.MemoKind, "scope": request.Scope, "configuration": request.Configuration, "content": request.Content, "invalidation_condition": request.InvalidationCondition})
	if len(payload) > maximumRecordPayload {
		return MutationResponse{}, contracts.ErrLimitExceeded
	}
	record, err := s.mutate(ctx, "memo", "memo.v1", request.Source, request.Mode, request.ID, request.ExpectedRevision, payload, request.EvidenceRefs)
	return s.mutationResponse(record, false, err)
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
	if len(request.Path) > 4096 || len(request.ChecklistPath) > 4096 || len(request.Configuration) > 128 {
		return MutationResponse{}, contracts.ErrLimitExceeded
	}
	if limits.ByteLimit > maximumReportBytes {
		return MutationResponse{}, contracts.ErrLimitExceeded
	}
	ctx, cancel := context.WithTimeout(ctx, limits.TimeLimit)
	defer cancel()
	path, err := s.root.ResolveExisting(filepath.FromSlash(request.Path))
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
	return s.mutationResponse(record, duplicate, err)
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
			return s.records.CreateCheckpoint(ctx, store.RecordCreate{Record: record})
		}
		return s.records.CreateMemo(ctx, store.RecordCreate{Record: record})
	case "update", "supersede":
		validity := "current"
		if mode == "supersede" {
			validity = "superseded"
		}
		update := store.RecordUpdate{ProjectID: s.projectID, WorkspaceID: s.workspaceID, ID: id, ExpectedRevision: expected, Payload: payload, EvidenceRefs: nonNil(evidence), Validity: validity}
		if kind == "checkpoint" {
			return s.records.UpdateCheckpoint(ctx, update)
		}
		return s.records.UpdateMemo(ctx, update)
	default:
		return store.Record{}, fmt.Errorf("unsupported mutation mode %q", mode)
	}
}

func (s *Service) mutationResponse(record store.Record, duplicate bool, err error) (MutationResponse, error) {
	if err != nil {
		return MutationResponse{}, err
	}
	result, err := s.result(record, nil)
	if err != nil {
		return MutationResponse{}, err
	}
	return MutationResponse{Status: contracts.StatusOK, Record: result, Duplicate: duplicate, Warnings: contracts.EmptyWarnings()}, nil
}

func (s *Service) result(record store.Record, current *subjectObservation) (RecordResult, error) {
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
	return RecordResult{ID: record.ID, Kind: record.Kind, SchemaVersion: record.SchemaVersion, Revision: record.Revision, Source: record.Source, WriterClass: record.WriterClass, Validity: validity, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt, Payload: payload, EvidenceRefs: nonNil(record.EvidenceRefs), Supersedes: record.Supersedes}, nil
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
	return nil
}

func validateCheckpoint(request CheckpointRequest) error {
	if request.Mode != "create" && request.Mode != "update" && request.Mode != "supersede" {
		return errors.New("mode must be create, update, or supersede")
	}
	if request.Mode == "create" && strings.TrimSpace(request.Goal) == "" {
		return errors.New("goal is required for create")
	}
	if request.Mode != "create" && (request.ID == "" || request.ExpectedRevision == 0) {
		return errors.New("id and expected_revision are required for update or supersede")
	}
	return validateTextAndRefs(request.Goal+request.BaselineCommit+request.NextAction, append(append(request.RunRefs, request.RemainingChecks...), append(request.Risks, request.EvidenceRefs...)...))
}

func validateMemo(request MemoRequest) error {
	if request.Mode != "create" && request.Mode != "update" && request.Mode != "supersede" {
		return errors.New("mode must be create, update, or supersede")
	}
	allowedKind := request.MemoKind == "decision" || request.MemoKind == "failed_attempt" || request.MemoKind == "resolved_failure" || request.MemoKind == "limitation"
	if request.Mode != "supersede" && (!allowedKind || strings.TrimSpace(request.Content) == "") {
		return errors.New("memo_kind and content are required")
	}
	if request.Source != "user_asserted" && request.Source != "llm_proposed" {
		return errors.New("source must be user_asserted or llm_proposed")
	}
	if request.Mode != "create" && (request.ID == "" || request.ExpectedRevision == 0) {
		return errors.New("id and expected_revision are required for update or supersede")
	}
	return validateTextAndRefs(request.MemoKind+request.Scope+request.Configuration+request.Content+request.InvalidationCondition, request.EvidenceRefs)
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
	path, err := s.root.ResolveExisting(filepath.FromSlash(relative))
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
