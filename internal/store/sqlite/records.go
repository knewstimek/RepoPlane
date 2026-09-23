package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"repoplane/internal/store"
)

var _ store.RecordRepository = (*RecordRepository)(nil)

type RecordRepository struct{ db *sql.DB }

func OpenRecords(ctx context.Context, path string) (*RecordRepository, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open durable records sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	repository := &RecordRepository{db: db}
	if err := repository.initialize(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return repository, nil
}

func (r *RecordRepository) Close() error { return r.db.Close() }

const recordsSchemaVersion = 4

func (r *RecordRepository) initialize(ctx context.Context) (err error) {
	for _, statement := range []string{`PRAGMA foreign_keys = ON`, `PRAGMA busy_timeout = 5000`} {
		if _, err := r.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure durable records sqlite: %w", err)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin durable records migration: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("initialize durable migration ledger: %w", err)
	}
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read durable schema version: %w", err)
	}
	if version > recordsSchemaVersion {
		return fmt.Errorf("durable records schema version %d is newer than supported version %d", version, recordsSchemaVersion)
	}
	if version < 1 {
		for _, statement := range []string{
			`CREATE TABLE records (
                id TEXT PRIMARY KEY,
                kind TEXT NOT NULL,
                schema_version TEXT NOT NULL,
                project_id TEXT NOT NULL,
                workspace_id TEXT NOT NULL,
                revision INTEGER NOT NULL,
                source TEXT NOT NULL,
                writer_class TEXT NOT NULL,
                validity TEXT NOT NULL,
                created_at INTEGER NOT NULL,
                updated_at INTEGER NOT NULL,
                payload_json BLOB NOT NULL,
                evidence_json BLOB NOT NULL,
                supersedes TEXT NOT NULL DEFAULT '')`,
			`CREATE INDEX records_query ON records(project_id, workspace_id, kind, validity, source, updated_at DESC, id)`,
			`CREATE TABLE record_revisions (
                record_id TEXT NOT NULL REFERENCES records(id) ON DELETE CASCADE,
                revision INTEGER NOT NULL,
                updated_at INTEGER NOT NULL,
                payload_json BLOB NOT NULL,
                evidence_json BLOB NOT NULL,
                validity TEXT NOT NULL,
                supersedes TEXT NOT NULL,
                PRIMARY KEY(record_id, revision))`,
			`CREATE TABLE report_imports (
                project_id TEXT NOT NULL,
                workspace_id TEXT NOT NULL,
                source_hash TEXT NOT NULL,
                parser_revision TEXT NOT NULL,
                record_id TEXT NOT NULL REFERENCES records(id),
                imported_at INTEGER NOT NULL,
                PRIMARY KEY(project_id, workspace_id, source_hash, parser_revision))`,
		} {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply durable records migration 1: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (1, ?)`, time.Now().UTC().UnixNano()); err != nil {
			return fmt.Errorf("record durable migration 1: %w", err)
		}
	}
	if version < 2 {
		for _, statement := range []string{
			`CREATE UNIQUE INDEX records_current_memo_topic ON records(
                project_id,
                workspace_id,
                json_extract(CAST(payload_json AS TEXT), '$.scope'),
                json_extract(CAST(payload_json AS TEXT), '$.configuration'),
                json_extract(CAST(payload_json AS TEXT), '$.topic_key'))
              WHERE kind='memo' AND validity='current'
                AND json_type(CAST(payload_json AS TEXT), '$.topic_key')='text'
                AND json_extract(CAST(payload_json AS TEXT), '$.topic_key')<>''`,
			`CREATE TRIGGER records_memo_topic_identity_immutable
              BEFORE UPDATE OF payload_json ON records
              WHEN OLD.kind='memo' AND OLD.validity='current' AND NEW.validity='current'
                AND (
                  COALESCE(json_extract(CAST(OLD.payload_json AS TEXT), '$.topic_key'), '') <>
                    COALESCE(json_extract(CAST(NEW.payload_json AS TEXT), '$.topic_key'), '')
                  OR (
                    COALESCE(json_extract(CAST(OLD.payload_json AS TEXT), '$.topic_key'), '') <> ''
                    AND (
                      COALESCE(json_extract(CAST(OLD.payload_json AS TEXT), '$.scope'), '') <>
                        COALESCE(json_extract(CAST(NEW.payload_json AS TEXT), '$.scope'), '')
                      OR COALESCE(json_extract(CAST(OLD.payload_json AS TEXT), '$.configuration'), '') <>
                        COALESCE(json_extract(CAST(NEW.payload_json AS TEXT), '$.configuration'), '')
                    )
                  )
                )
              BEGIN SELECT RAISE(ABORT, 'memo topic identity is immutable'); END`,
		} {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply durable records migration 2: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (2, ?)`, time.Now().UTC().UnixNano()); err != nil {
			return fmt.Errorf("record durable migration 2: %w", err)
		}
	}
	if version < 3 {
		if _, err = tx.ExecContext(ctx, `CREATE INDEX records_memo_topic_lookup ON records(
            project_id, workspace_id,
            json_extract(CAST(payload_json AS TEXT), '$.topic_key'),
            json_extract(CAST(payload_json AS TEXT), '$.scope'),
            json_extract(CAST(payload_json AS TEXT), '$.configuration'))
          WHERE kind='memo' AND validity='current'
            AND json_type(CAST(payload_json AS TEXT), '$.topic_key')='text'`); err != nil {
			return fmt.Errorf("apply durable records migration 3: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (3, ?)`, time.Now().UTC().UnixNano()); err != nil {
			return fmt.Errorf("record durable migration 3: %w", err)
		}
	}
	if version < 4 {
		if _, err = tx.ExecContext(ctx, `CREATE UNIQUE INDEX records_memo_successor ON records(
            project_id, workspace_id, supersedes)
          WHERE kind='memo' AND supersedes<>''`); err != nil {
			return fmt.Errorf("apply durable records migration 4: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (4, ?)`, time.Now().UTC().UnixNano()); err != nil {
			return fmt.Errorf("record durable migration 4: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit durable records migrations: %w", err)
	}
	return nil
}

func (r *RecordRepository) GetRecord(ctx context.Context, projectID, workspaceID, id string) (store.Record, error) {
	row := r.db.QueryRowContext(ctx, recordSelect+` WHERE project_id=? AND workspace_id=? AND id=?`, projectID, workspaceID, id)
	return scanRecord(row)
}

func (r *RecordRepository) QueryRecords(ctx context.Context, query store.RecordQuery) (store.RecordPage, error) {
	if query.ProjectID == "" || query.WorkspaceID == "" || query.Limit == 0 || query.Limit > 10_000 || len(query.Terms) > 16 {
		return store.RecordPage{}, fmt.Errorf("query durable records: %w", store.ErrConflict)
	}
	where := []string{"project_id=?", "workspace_id=?"}
	args := []any{query.ProjectID, query.WorkspaceID}
	for _, filter := range []struct{ column, value string }{{"kind", query.Kind}, {"validity", query.Validity}, {"source", query.Source}} {
		column, value := filter.column, filter.value
		if value != "" {
			where = append(where, column+"=?")
			args = append(args, value)
		}
	}
	if query.Supersedes != "" {
		where = append(where, "supersedes=?")
		args = append(args, query.Supersedes)
	}
	if !query.UpdatedAfter.IsZero() {
		where = append(where, "updated_at>=?")
		args = append(args, unixNano(query.UpdatedAfter))
	}
	if !query.UpdatedBefore.IsZero() {
		where = append(where, "updated_at<?")
		args = append(args, unixNano(query.UpdatedBefore))
	}
	for _, filter := range []struct{ field, value string }{
		{"topic_key", query.TopicKey}, {"scope", query.Scope}, {"configuration", query.Configuration},
	} {
		if filter.value != "" {
			if filter.field == "topic_key" {
				where = append(where, "json_type(CAST(payload_json AS TEXT), '$.topic_key')='text'")
			}
			where = append(where, "json_extract(CAST(payload_json AS TEXT), '$."+filter.field+"')=?")
			args = append(args, filter.value)
		}
	}
	metadataText := "lower(id || ' ' || kind || ' ' || schema_version || ' ' || source)"
	payloadMatch := "EXISTS (SELECT 1 FROM json_tree(CAST(payload_json AS TEXT)) AS value WHERE value.type='text' AND instr(lower(CAST(value.value AS TEXT)), ?) > 0)"
	score := make([]string, 0, len(query.Terms))
	coverage := make([]string, 0, len(query.Terms))
	coverageArgs := make([]any, 0, len(query.Terms)*2)
	scoreArgs := make([]any, 0, len(query.Terms)*3)
	if len(query.Terms) > 0 {
		matches := make([]string, 0, len(query.Terms))
		for _, term := range query.Terms {
			term = strings.ToLower(strings.TrimSpace(term))
			if term == "" || len(term) > 128 {
				return store.RecordPage{}, fmt.Errorf("query durable records: %w", store.ErrConflict)
			}
			matches = append(matches, "(instr("+metadataText+", ?) > 0 OR "+payloadMatch+")")
			args = append(args, term, term)
			coverage = append(coverage, "CASE WHEN instr("+metadataText+", ?) > 0 OR "+payloadMatch+" THEN 1 ELSE 0 END")
			coverageArgs = append(coverageArgs, term, term)
			score = append(score, "CASE WHEN instr(lower(id), ?) > 0 THEN 8 WHEN instr("+metadataText+", ?) > 0 THEN 4 WHEN "+payloadMatch+" THEN 1 ELSE 0 END")
			scoreArgs = append(scoreArgs, term, term, term)
		}
		join := " OR "
		if query.MatchAll {
			join = " AND "
		}
		where = append(where, "("+strings.Join(matches, join)+")")
	}
	clause := strings.Join(where, " AND ")
	var matched uint64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM records WHERE `+clause, args...).Scan(&matched); err != nil {
		return store.RecordPage{}, fmt.Errorf("count durable records: %w", err)
	}
	order := "updated_at DESC, id"
	if len(score) > 0 {
		order = "CASE WHEN validity='current' THEN 1 ELSE 0 END DESC, (" + strings.Join(coverage, " + ") + ") DESC, (" + strings.Join(score, " + ") + ") DESC, " + order
		args = append(args, coverageArgs...)
		args = append(args, scoreArgs...)
	}
	args = append(args, query.Limit)
	rows, err := r.db.QueryContext(ctx, recordSelect+` WHERE `+clause+` ORDER BY `+order+` LIMIT ?`, args...)
	if err != nil {
		return store.RecordPage{}, fmt.Errorf("query durable records: %w", err)
	}
	defer rows.Close()
	result := store.RecordPage{Records: make([]store.Record, 0), Matched: matched, Complete: matched <= query.Limit}
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return store.RecordPage{}, err
		}
		result.Records = append(result.Records, record)
	}
	if err := rows.Err(); err != nil {
		return store.RecordPage{}, fmt.Errorf("iterate durable records: %w", err)
	}
	return result, nil
}

func (r *RecordRepository) ExportRecords(ctx context.Context, projectID, workspaceID string, limit uint64) (store.RecordArchive, error) {
	if projectID == "" || workspaceID == "" || limit == 0 || limit > 100_000 {
		return store.RecordArchive{}, store.ErrConflict
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return store.RecordArchive{}, fmt.Errorf("begin durable record export: %w", err)
	}
	defer tx.Rollback()
	var recordCount, revisionCount, importCount uint64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM records WHERE project_id=? AND workspace_id=?`, projectID, workspaceID).Scan(&recordCount); err != nil {
		return store.RecordArchive{}, fmt.Errorf("count exported records: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM record_revisions rr JOIN records r ON r.id=rr.record_id WHERE r.project_id=? AND r.workspace_id=?`, projectID, workspaceID).Scan(&revisionCount); err != nil {
		return store.RecordArchive{}, fmt.Errorf("count exported revisions: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM report_imports WHERE project_id=? AND workspace_id=?`, projectID, workspaceID).Scan(&importCount); err != nil {
		return store.RecordArchive{}, fmt.Errorf("count exported imports: %w", err)
	}
	if recordCount+revisionCount+importCount > limit {
		return store.RecordArchive{}, fmt.Errorf("durable record export exceeds item limit: %w", store.ErrConflict)
	}
	archive := store.RecordArchive{Records: make([]store.Record, 0, recordCount), Revisions: make([]store.RecordRevision, 0, revisionCount), Imports: make([]store.ReportReceipt, 0, importCount)}
	rows, err := tx.QueryContext(ctx, recordSelect+` WHERE project_id=? AND workspace_id=? ORDER BY id`, projectID, workspaceID)
	if err != nil {
		return store.RecordArchive{}, fmt.Errorf("export durable records: %w", err)
	}
	for rows.Next() {
		record, scanErr := scanRecord(rows)
		if scanErr != nil {
			_ = rows.Close()
			return store.RecordArchive{}, scanErr
		}
		archive.Records = append(archive.Records, record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return store.RecordArchive{}, fmt.Errorf("iterate exported records: %w", err)
	}
	if err := rows.Close(); err != nil {
		return store.RecordArchive{}, fmt.Errorf("close exported records: %w", err)
	}
	rows, err = tx.QueryContext(ctx, `SELECT rr.record_id, rr.revision, rr.updated_at, rr.payload_json, rr.evidence_json, rr.validity, rr.supersedes
		FROM record_revisions rr JOIN records r ON r.id=rr.record_id
		WHERE r.project_id=? AND r.workspace_id=? ORDER BY rr.record_id, rr.revision`, projectID, workspaceID)
	if err != nil {
		return store.RecordArchive{}, fmt.Errorf("export durable revisions: %w", err)
	}
	for rows.Next() {
		var item store.RecordRevision
		var updated int64
		var payload, evidence []byte
		if err := rows.Scan(&item.RecordID, &item.Revision, &updated, &payload, &evidence, &item.Validity, &item.Supersedes); err != nil {
			_ = rows.Close()
			return store.RecordArchive{}, fmt.Errorf("scan exported revision: %w", err)
		}
		item.UpdatedAt = time.Unix(0, updated).UTC()
		item.Payload = append(json.RawMessage(nil), payload...)
		if err := json.Unmarshal(evidence, &item.EvidenceRefs); err != nil {
			_ = rows.Close()
			return store.RecordArchive{}, fmt.Errorf("decode exported revision evidence: %w", err)
		}
		archive.Revisions = append(archive.Revisions, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return store.RecordArchive{}, fmt.Errorf("iterate exported revisions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return store.RecordArchive{}, fmt.Errorf("close exported revisions: %w", err)
	}
	rows, err = tx.QueryContext(ctx, `SELECT source_hash, parser_revision, record_id, imported_at FROM report_imports WHERE project_id=? AND workspace_id=? ORDER BY source_hash, parser_revision`, projectID, workspaceID)
	if err != nil {
		return store.RecordArchive{}, fmt.Errorf("export report receipts: %w", err)
	}
	for rows.Next() {
		var item store.ReportReceipt
		var imported int64
		if err := rows.Scan(&item.SourceHash, &item.ParserRevision, &item.RecordID, &imported); err != nil {
			_ = rows.Close()
			return store.RecordArchive{}, fmt.Errorf("scan exported report receipt: %w", err)
		}
		item.ImportedAt = time.Unix(0, imported).UTC()
		archive.Imports = append(archive.Imports, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return store.RecordArchive{}, fmt.Errorf("iterate exported report receipts: %w", err)
	}
	if err := rows.Close(); err != nil {
		return store.RecordArchive{}, fmt.Errorf("close exported report receipts: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return store.RecordArchive{}, fmt.Errorf("finish durable record export: %w", err)
	}
	return archive, nil
}

func (r *RecordRepository) RestoreRecords(ctx context.Context, projectID, workspaceID string, archive store.RecordArchive) error {
	if projectID == "" || workspaceID == "" || len(archive.Records)+len(archive.Revisions)+len(archive.Imports) > 100_000 {
		return store.ErrConflict
	}
	if err := validateRecordArchive(archive); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin durable record restore: %w", err)
	}
	defer tx.Rollback()
	var existing uint64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM records WHERE project_id=? AND workspace_id=?`, projectID, workspaceID).Scan(&existing); err != nil {
		return fmt.Errorf("check restore target: %w", err)
	}
	if existing != 0 {
		return fmt.Errorf("restore target already has durable records: %w", store.ErrConflict)
	}
	recordIDs := make(map[string]struct{}, len(archive.Records))
	for _, record := range archive.Records {
		record.ProjectID, record.WorkspaceID = projectID, workspaceID
		recordIDs[record.ID] = struct{}{}
		evidence, _ := json.Marshal(record.EvidenceRefs)
		if _, err := tx.ExecContext(ctx, `INSERT INTO records(id, kind, schema_version, project_id, workspace_id, revision, source, writer_class, validity, created_at, updated_at, payload_json, evidence_json, supersedes) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.ID, record.Kind, record.SchemaVersion, record.ProjectID, record.WorkspaceID, record.Revision, record.Source, record.WriterClass, record.Validity, unixNano(record.CreatedAt), unixNano(record.UpdatedAt), []byte(record.Payload), evidence, record.Supersedes); err != nil {
			return fmt.Errorf("restore durable record: %w", err)
		}
	}
	for _, revision := range archive.Revisions {
		if _, ok := recordIDs[revision.RecordID]; !ok || revision.Revision == 0 || revision.UpdatedAt.IsZero() || !json.Valid(revision.Payload) || len(revision.Payload) > 64*1024 || len(revision.EvidenceRefs) > 128 || revision.Validity == "" {
			return fmt.Errorf("restore revision is invalid: %w", store.ErrConflict)
		}
		evidence, _ := json.Marshal(revision.EvidenceRefs)
		if _, err := tx.ExecContext(ctx, `INSERT INTO record_revisions(record_id, revision, updated_at, payload_json, evidence_json, validity, supersedes) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			revision.RecordID, revision.Revision, unixNano(revision.UpdatedAt), []byte(revision.Payload), evidence, revision.Validity, revision.Supersedes); err != nil {
			return fmt.Errorf("restore durable revision: %w", err)
		}
	}
	for _, receipt := range archive.Imports {
		if _, ok := recordIDs[receipt.RecordID]; !ok || receipt.SourceHash == "" || receipt.ParserRevision == "" || receipt.ImportedAt.IsZero() {
			return fmt.Errorf("restore report receipt is invalid: %w", store.ErrConflict)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO report_imports(project_id, workspace_id, source_hash, parser_revision, record_id, imported_at) VALUES (?, ?, ?, ?, ?, ?)`,
			projectID, workspaceID, receipt.SourceHash, receipt.ParserRevision, receipt.RecordID, unixNano(receipt.ImportedAt)); err != nil {
			return fmt.Errorf("restore report receipt: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit durable record restore: %w", err)
	}
	return nil
}

func validateRecordArchive(archive store.RecordArchive) error {
	records := make(map[string]store.Record, len(archive.Records))
	ownerProject, ownerWorkspace := "", ""
	for _, record := range archive.Records {
		if record.ID == "" || record.Kind == "" || record.SchemaVersion == "" || record.ProjectID == "" || record.WorkspaceID == "" || record.Revision == 0 || record.Source == "" || record.WriterClass == "" || record.Validity == "" || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || !json.Valid(record.Payload) || len(record.Payload) > 64*1024 || len(record.EvidenceRefs) > 128 {
			return fmt.Errorf("restore record is invalid: %w", store.ErrConflict)
		}
		if ownerProject == "" {
			ownerProject, ownerWorkspace = record.ProjectID, record.WorkspaceID
		}
		if record.ProjectID != ownerProject || record.WorkspaceID != ownerWorkspace {
			return fmt.Errorf("restore archive has mixed ownership: %w", store.ErrConflict)
		}
		if _, duplicate := records[record.ID]; duplicate {
			return fmt.Errorf("restore record ID is duplicated: %w", store.ErrConflict)
		}
		records[record.ID] = record
	}
	revisions := make(map[string]map[uint64]store.RecordRevision, len(records))
	for _, revision := range archive.Revisions {
		if _, ok := records[revision.RecordID]; !ok || revision.Revision == 0 || revision.UpdatedAt.IsZero() || !json.Valid(revision.Payload) || len(revision.Payload) > 64*1024 || len(revision.EvidenceRefs) > 128 || revision.Validity == "" {
			return fmt.Errorf("restore revision is invalid: %w", store.ErrConflict)
		}
		byNumber := revisions[revision.RecordID]
		if byNumber == nil {
			byNumber = make(map[uint64]store.RecordRevision)
			revisions[revision.RecordID] = byNumber
		}
		if _, duplicate := byNumber[revision.Revision]; duplicate {
			return fmt.Errorf("restore revision is duplicated: %w", store.ErrConflict)
		}
		byNumber[revision.Revision] = revision
	}
	for id, record := range records {
		byNumber := revisions[id]
		if uint64(len(byNumber)) != record.Revision {
			return fmt.Errorf("restore revision history is incomplete: %w", store.ErrConflict)
		}
		for revision := uint64(1); revision <= record.Revision; revision++ {
			if _, ok := byNumber[revision]; !ok {
				return fmt.Errorf("restore revision history is incomplete: %w", store.ErrConflict)
			}
		}
		latest := byNumber[record.Revision]
		if !latest.UpdatedAt.Equal(record.UpdatedAt) || !bytes.Equal(latest.Payload, record.Payload) || latest.Validity != record.Validity || latest.Supersedes != record.Supersedes || !equalStrings(latest.EvidenceRefs, record.EvidenceRefs) {
			return fmt.Errorf("restore current record disagrees with revision history: %w", store.ErrConflict)
		}
	}
	for _, receipt := range archive.Imports {
		record, ok := records[receipt.RecordID]
		if !ok || record.Kind != "verification" || receipt.SourceHash == "" || receipt.ParserRevision == "" || receipt.ImportedAt.IsZero() {
			return fmt.Errorf("restore report receipt is invalid: %w", store.ErrConflict)
		}
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (r *RecordRepository) CreateCheckpoint(ctx context.Context, create store.RecordCreate) (store.Record, error) {
	if create.Record.Kind != "checkpoint" || create.Record.WriterClass != "intention" {
		return store.Record{}, store.ErrConflict
	}
	return r.create(ctx, create.Record)
}

func (r *RecordRepository) UpdateCheckpoint(ctx context.Context, update store.RecordUpdate) (store.Record, error) {
	return r.update(ctx, "checkpoint", update)
}

func (r *RecordRepository) CreateMemo(ctx context.Context, create store.RecordCreate) (store.Record, error) {
	if create.Record.Kind != "memo" || create.Record.WriterClass != "intention" {
		return store.Record{}, store.ErrConflict
	}
	return r.create(ctx, create.Record)
}

func (r *RecordRepository) UpdateMemo(ctx context.Context, update store.RecordUpdate) (store.Record, error) {
	return r.update(ctx, "memo", update)
}

func (r *RecordRepository) ReplaceMemo(ctx context.Context, replace store.RecordReplace) (store.Record, error) {
	if replace.ProjectID == "" || replace.WorkspaceID == "" || replace.OldID == "" || replace.ExpectedRevision == 0 ||
		replace.New.Kind != "memo" || replace.New.Supersedes != replace.OldID ||
		replace.New.ProjectID != replace.ProjectID || replace.New.WorkspaceID != replace.WorkspaceID {
		return store.Record{}, store.ErrConflict
	}
	if err := validateRecord(replace.New); err != nil {
		return store.Record{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return store.Record{}, fmt.Errorf("begin memo replacement: %w", err)
	}
	defer tx.Rollback()
	old, err := getRecordTx(ctx, tx, replace.ProjectID, replace.WorkspaceID, replace.OldID)
	if err != nil {
		return store.Record{}, err
	}
	if old.Kind != "memo" || old.Validity != "current" || old.Revision != replace.ExpectedRevision {
		return store.Record{}, store.ErrConflict
	}
	old.Revision++
	old.UpdatedAt = replace.New.CreatedAt
	old.Validity = "superseded"
	result, err := tx.ExecContext(ctx, `UPDATE records SET revision=?, updated_at=?, validity=?
        WHERE id=? AND project_id=? AND workspace_id=? AND revision=? AND validity='current'`,
		old.Revision, unixNano(old.UpdatedAt), old.Validity, old.ID, old.ProjectID, old.WorkspaceID, replace.ExpectedRevision)
	if err != nil {
		return store.Record{}, fmt.Errorf("supersede replaced memo: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return store.Record{}, store.ErrConflict
	}
	evidence, _ := json.Marshal(old.EvidenceRefs)
	if _, err := tx.ExecContext(ctx, `INSERT INTO record_revisions(record_id, revision, updated_at, payload_json, evidence_json, validity, supersedes)
        VALUES (?, ?, ?, ?, ?, ?, ?)`, old.ID, old.Revision, unixNano(old.UpdatedAt), []byte(old.Payload), evidence, old.Validity, old.Supersedes); err != nil {
		return store.Record{}, fmt.Errorf("record replaced memo revision: %w", err)
	}
	if err := insertRecord(ctx, tx, replace.New); err != nil {
		return store.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return store.Record{}, fmt.Errorf("commit memo replacement: %w", err)
	}
	return replace.New, nil
}

func (r *RecordRepository) ImportVerification(ctx context.Context, create store.RecordCreate, sourceHash, parserRevision string) (store.Record, bool, error) {
	if create.Record.Kind != "verification" || create.Record.WriterClass != "importer" || sourceHash == "" || parserRevision == "" {
		return store.Record{}, false, store.ErrConflict
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return store.Record{}, false, fmt.Errorf("begin report import: %w", err)
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT record_id FROM report_imports WHERE project_id=? AND workspace_id=? AND source_hash=? AND parser_revision=?`,
		create.Record.ProjectID, create.Record.WorkspaceID, sourceHash, parserRevision).Scan(&existing)
	if err == nil {
		record, getErr := getRecordTx(ctx, tx, create.Record.ProjectID, create.Record.WorkspaceID, existing)
		return record, true, getErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.Record{}, false, fmt.Errorf("check duplicate report import: %w", err)
	}
	if err := insertRecord(ctx, tx, create.Record); err != nil {
		return store.Record{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO report_imports(project_id, workspace_id, source_hash, parser_revision, record_id, imported_at) VALUES (?, ?, ?, ?, ?, ?)`,
		create.Record.ProjectID, create.Record.WorkspaceID, sourceHash, parserRevision, create.Record.ID, time.Now().UTC().UnixNano()); err != nil {
		return store.Record{}, false, fmt.Errorf("insert report import receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return store.Record{}, false, fmt.Errorf("commit report import: %w", err)
	}
	return create.Record, false, nil
}

func (r *RecordRepository) CreateObservation(ctx context.Context, create store.RecordCreate) (store.Record, error) {
	if !observationKind(create.Record.Kind) || create.Record.WriterClass != "server" || create.Record.Source != "observed" {
		return store.Record{}, store.ErrConflict
	}
	return r.create(ctx, create.Record)
}

func (r *RecordRepository) UpdateObservation(ctx context.Context, kind string, update store.RecordUpdate) (store.Record, error) {
	if !observationKind(kind) {
		return store.Record{}, store.ErrConflict
	}
	return r.update(ctx, kind, update)
}

func observationKind(kind string) bool {
	return kind == "environment" || kind == "run" || kind == "artifact"
}

func (r *RecordRepository) create(ctx context.Context, record store.Record) (store.Record, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return store.Record{}, err
	}
	defer tx.Rollback()
	if err := insertRecord(ctx, tx, record); err != nil {
		return store.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return store.Record{}, fmt.Errorf("commit durable record: %w", err)
	}
	return record, nil
}

func insertRecord(ctx context.Context, tx *sql.Tx, record store.Record) error {
	if err := validateRecord(record); err != nil {
		return err
	}
	evidence, _ := json.Marshal(record.EvidenceRefs)
	_, err := tx.ExecContext(ctx, `INSERT INTO records(id, kind, schema_version, project_id, workspace_id, revision, source, writer_class, validity, created_at, updated_at, payload_json, evidence_json, supersedes) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.Kind, record.SchemaVersion, record.ProjectID, record.WorkspaceID, record.Revision, record.Source, record.WriterClass, record.Validity,
		unixNano(record.CreatedAt), unixNano(record.UpdatedAt), []byte(record.Payload), evidence, record.Supersedes)
	if err != nil {
		return fmt.Errorf("insert durable record: %w: %v", store.ErrConflict, err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO record_revisions(record_id, revision, updated_at, payload_json, evidence_json, validity, supersedes) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.Revision, unixNano(record.UpdatedAt), []byte(record.Payload), evidence, record.Validity, record.Supersedes)
	if err != nil {
		return fmt.Errorf("insert initial record revision: %w", err)
	}
	return nil
}

func (r *RecordRepository) update(ctx context.Context, kind string, update store.RecordUpdate) (store.Record, error) {
	if update.ProjectID == "" || update.WorkspaceID == "" || update.ID == "" || update.ExpectedRevision == 0 || update.Validity == "" || !json.Valid(update.Payload) || len(update.Payload) > 64*1024 || len(update.EvidenceRefs) > 128 {
		return store.Record{}, store.ErrConflict
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return store.Record{}, err
	}
	defer tx.Rollback()
	current, err := getRecordTx(ctx, tx, update.ProjectID, update.WorkspaceID, update.ID)
	if err != nil {
		return store.Record{}, err
	}
	if current.Kind != kind || current.Revision != update.ExpectedRevision {
		return store.Record{}, store.ErrConflict
	}
	current.Revision++
	current.UpdatedAt = time.Now().UTC()
	current.Payload = append(json.RawMessage(nil), update.Payload...)
	current.EvidenceRefs = append([]string(nil), update.EvidenceRefs...)
	current.Validity = update.Validity
	current.Supersedes = update.Supersedes
	evidence, _ := json.Marshal(current.EvidenceRefs)
	result, err := tx.ExecContext(ctx, `UPDATE records SET revision=?, updated_at=?, payload_json=?, evidence_json=?, validity=?, supersedes=? WHERE id=? AND project_id=? AND workspace_id=? AND revision=?`,
		current.Revision, unixNano(current.UpdatedAt), []byte(current.Payload), evidence, current.Validity, current.Supersedes,
		current.ID, current.ProjectID, current.WorkspaceID, update.ExpectedRevision)
	if err != nil {
		if strings.Contains(err.Error(), "memo topic identity is immutable") {
			return store.Record{}, fmt.Errorf("update durable record: %w: memo topic identity is immutable", store.ErrConflict)
		}
		return store.Record{}, fmt.Errorf("update durable record: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return store.Record{}, store.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO record_revisions(record_id, revision, updated_at, payload_json, evidence_json, validity, supersedes) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		current.ID, current.Revision, unixNano(current.UpdatedAt), []byte(current.Payload), evidence, current.Validity, current.Supersedes); err != nil {
		return store.Record{}, fmt.Errorf("insert durable record revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return store.Record{}, fmt.Errorf("commit durable record update: %w", err)
	}
	return current, nil
}

const recordSelect = `SELECT id, kind, schema_version, project_id, workspace_id, revision, source, writer_class, validity, created_at, updated_at, payload_json, evidence_json, supersedes FROM records`

type rowScanner interface{ Scan(...any) error }

func scanRecord(row rowScanner) (store.Record, error) {
	var result store.Record
	var created, updated int64
	var payload, evidence []byte
	err := row.Scan(&result.ID, &result.Kind, &result.SchemaVersion, &result.ProjectID, &result.WorkspaceID, &result.Revision,
		&result.Source, &result.WriterClass, &result.Validity, &created, &updated, &payload, &evidence, &result.Supersedes)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Record{}, store.ErrNotFound
	}
	if err != nil {
		return store.Record{}, fmt.Errorf("scan durable record: %w", err)
	}
	result.CreatedAt = time.Unix(0, created).UTC()
	result.UpdatedAt = time.Unix(0, updated).UTC()
	result.Payload = append(json.RawMessage(nil), payload...)
	if err := json.Unmarshal(evidence, &result.EvidenceRefs); err != nil {
		return store.Record{}, fmt.Errorf("decode record evidence: %w", err)
	}
	if result.EvidenceRefs == nil {
		result.EvidenceRefs = []string{}
	}
	return result, nil
}

func getRecordTx(ctx context.Context, tx *sql.Tx, projectID, workspaceID, id string) (store.Record, error) {
	return scanRecord(tx.QueryRowContext(ctx, recordSelect+` WHERE project_id=? AND workspace_id=? AND id=?`, projectID, workspaceID, id))
}

func validateRecord(record store.Record) error {
	if record.ID == "" || record.Kind == "" || record.SchemaVersion == "" || record.ProjectID == "" || record.WorkspaceID == "" || record.Revision != 1 || record.Source == "" || record.WriterClass == "" || record.Validity == "" || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || !json.Valid(record.Payload) || len(record.Payload) > 64*1024 || len(record.EvidenceRefs) > 128 {
		return store.ErrConflict
	}
	return nil
}
