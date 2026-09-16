package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"repoplane/internal/store"
)

const usageRetentionDays = 365

type UsageDB struct{ db *sql.DB }

func OpenUsage(ctx context.Context, path string) (*UsageDB, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create usage store: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS usage_daily (
day TEXT NOT NULL, workspace_id TEXT NOT NULL, transport TEXT NOT NULL, tool TEXT NOT NULL,
outcome TEXT NOT NULL, calls INTEGER NOT NULL, request_bytes INTEGER NOT NULL,
response_bytes INTEGER NOT NULL, duration_ns INTEGER NOT NULL,
PRIMARY KEY(day, workspace_id, transport, tool, outcome));`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize usage store: %w", err)
	}
	return &UsageDB{db: db}, nil
}

func (u *UsageDB) RecordUsage(ctx context.Context, event store.UsageEvent) error {
	day := event.At.UTC().Format("2006-01-02")
	_, err := u.db.ExecContext(ctx, `INSERT INTO usage_daily
(day, workspace_id, transport, tool, outcome, calls, request_bytes, response_bytes, duration_ns)
VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)
ON CONFLICT(day, workspace_id, transport, tool, outcome) DO UPDATE SET
calls=calls+1, request_bytes=request_bytes+excluded.request_bytes,
response_bytes=response_bytes+excluded.response_bytes, duration_ns=duration_ns+excluded.duration_ns`,
		day, event.WorkspaceID, event.Transport, event.Tool, event.Outcome,
		event.RequestBytes, event.ResponseBytes, event.DurationNS)
	if err != nil {
		return err
	}
	cutoff := event.At.UTC().AddDate(0, 0, -usageRetentionDays).Format("2006-01-02")
	_, err = u.db.ExecContext(ctx, `DELETE FROM usage_daily WHERE rowid IN
(SELECT rowid FROM usage_daily WHERE day < ? ORDER BY day LIMIT 256)`, cutoff)
	return err
}

func (u *UsageDB) QueryUsage(ctx context.Context, workspaceID, since, until string) ([]store.UsageAggregate, string, error) {
	var first sql.NullString
	if err := u.db.QueryRowContext(ctx, `SELECT MIN(day) FROM usage_daily WHERE workspace_id=?`, workspaceID).Scan(&first); err != nil {
		return nil, "", err
	}
	rows, err := u.db.QueryContext(ctx, `SELECT transport, tool, outcome, SUM(calls), SUM(request_bytes),
SUM(response_bytes), SUM(duration_ns) FROM usage_daily
WHERE workspace_id=? AND day>=? AND day<=? GROUP BY transport, tool, outcome
ORDER BY transport, tool, outcome`, workspaceID, since, until)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	result := make([]store.UsageAggregate, 0)
	for rows.Next() {
		var item store.UsageAggregate
		var durationNS uint64
		if err := rows.Scan(&item.Transport, &item.Tool, &item.Outcome, &item.Calls,
			&item.RequestBytes, &item.ResponseBytes, &durationNS); err != nil {
			return nil, "", err
		}
		item.DurationMS = durationNS / uint64(time.Millisecond)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	return result, first.String, nil
}

func (u *UsageDB) Close() error { return u.db.Close() }

var _ store.UsageRepository = (*UsageDB)(nil)
