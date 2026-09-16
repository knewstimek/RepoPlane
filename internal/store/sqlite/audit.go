package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"repoplane/internal/store"
)

type AuditDB struct{ db *sql.DB }

func OpenAudit(ctx context.Context, path string) (*AuditDB, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create audit store: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close audit store: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open audit store: %w", err)
	}
	db.SetMaxOpenConns(1)
	a := &AuditDB{db: db}
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS audit_events (
request_id TEXT PRIMARY KEY, principal_hash TEXT NOT NULL, workspace_id TEXT NOT NULL,
	method TEXT NOT NULL, tool TEXT NOT NULL, operation TEXT NOT NULL DEFAULT '', decision TEXT NOT NULL, status TEXT NOT NULL,
request_bytes INTEGER NOT NULL, response_bytes INTEGER NOT NULL DEFAULT 0,
started_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT ''); CREATE INDEX IF NOT EXISTS audit_events_started ON audit_events(started_at);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate audit store: %w", err)
	}
	if err := ensureAuditOperationColumn(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return a, nil
}

func ensureAuditOperationColumn(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(audit_events)`)
	if err != nil {
		return fmt.Errorf("inspect audit store: %w", err)
	}
	found := false
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("inspect audit column: %w", err)
		}
		found = found || name == "operation"
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close audit inspection: %w", err)
	}
	if found {
		return nil
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE audit_events ADD COLUMN operation TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("migrate audit operation: %w", err)
	}
	return nil
}

func (a *AuditDB) AdmitAudit(ctx context.Context, e store.AuditEvent) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin audit admission: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_events(request_id,principal_hash,workspace_id,method,tool,operation,decision,status,request_bytes,started_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, e.RequestID, e.PrincipalHash, e.WorkspaceID, e.Method, e.Tool, e.Operation, e.Decision, e.Status, e.RequestBytes, e.StartedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("write audit admission: %w", err)
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM audit_events WHERE request_id IN (SELECT request_id FROM audit_events ORDER BY started_at,request_id LIMIT 256) AND (SELECT COUNT(*) FROM audit_events) > 100000`)
	if err != nil {
		return fmt.Errorf("bound audit store: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit audit admission: %w", err)
	}
	return nil
}

func (a *AuditDB) ResolveAuditOperation(ctx context.Context, id, operation string) error {
	result, err := a.db.ExecContext(ctx, `UPDATE audit_events SET operation=? WHERE request_id=? AND operation=''`, operation, id)
	if err != nil {
		return fmt.Errorf("resolve audit operation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("resolve audit operation rows: %w", err)
	}
	if changed != 1 {
		return errors.New("resolve audit operation: request not found or already resolved")
	}
	return nil
}

func (a *AuditDB) CompleteAudit(ctx context.Context, id, status string, responseBytes uint64, completedAt time.Time) error {
	result, err := a.db.ExecContext(ctx, `UPDATE audit_events SET status=?,response_bytes=?,completed_at=? WHERE request_id=?`, status, responseBytes, completedAt.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("complete audit event: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return store.ErrNotFound
	}
	return nil
}

func (a *AuditDB) DeleteExpiredAudit(ctx context.Context, before time.Time, limit uint64) (uint64, error) {
	if limit == 0 || limit > 256 {
		return 0, errors.New("audit deletion limit must be 1..256")
	}
	result, err := a.db.ExecContext(ctx, `DELETE FROM audit_events WHERE request_id IN (SELECT request_id FROM audit_events WHERE started_at < ? ORDER BY started_at LIMIT ?)`, before.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired audit events: %w", err)
	}
	count, _ := result.RowsAffected()
	return uint64(count), nil
}

func (a *AuditDB) Close() error { return a.db.Close() }

var _ store.AuditRepository = (*AuditDB)(nil)
