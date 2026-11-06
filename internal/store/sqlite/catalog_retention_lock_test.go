package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestRepositoryUsesWriteAheadLog(t *testing.T) {
	r := openTestRepository(t)
	var mode string
	if err := r.db.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode=%q error=%v, want wal", mode, err)
	}
}

// An unchanged catalog refresh must not queue behind another process that
// holds the write lock when there is no retired generation to delete.
func TestRetiredCatalogCleanupWithoutBacklogSkipsWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repoplane.db")
	r, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	w := seedWorkspace(t, r)

	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	writer.SetMaxOpenConns(1)
	conn, err := writer.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	now := w.CreatedAt.Add(2 * time.Hour)
	started := time.Now()
	deleted, err := r.DeleteRetiredCatalogGenerations(ctx, w.ID, now.Add(-time.Hour), now, 16)
	elapsed := time.Since(started)
	if err != nil || deleted != 0 || elapsed > time.Second {
		t.Fatalf("deleted=%d error=%v elapsed=%s, want an immediate no-op", deleted, err, elapsed)
	}
}
