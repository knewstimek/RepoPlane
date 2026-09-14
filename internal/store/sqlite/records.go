package sqlite

import (
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

const recordsSchemaVersion = 1

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
	if query.ProjectID == "" || query.WorkspaceID == "" || query.Limit == 0 || query.Limit > 10_000 {
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
	if !query.UpdatedAfter.IsZero() {
		where = append(where, "updated_at>=?")
		args = append(args, unixNano(query.UpdatedAfter))
	}
	clause := strings.Join(where, " AND ")
	var matched uint64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM records WHERE `+clause, args...).Scan(&matched); err != nil {
		return store.RecordPage{}, fmt.Errorf("count durable records: %w", err)
	}
	args = append(args, query.Limit)
	rows, err := r.db.QueryContext(ctx, recordSelect+` WHERE `+clause+` ORDER BY updated_at DESC, id LIMIT ?`, args...)
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
