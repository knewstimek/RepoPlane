package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"repoplane/internal/store"
)

func TestLegacyCatalogWriterRemainsCompatibleAndRecordsFreshRetirement(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shared.db")
	r, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	w := seedWorkspace(t, r)
	past := time.Now().UTC().Add(-24 * time.Hour)
	g := generation(w.ID, "first", item("tool"))
	g.Meta.CreatedAt = past
	if err := r.PublishCatalogGeneration(ctx, g); err != nil {
		t.Fatal(err)
	}
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	legacy.SetMaxOpenConns(1)
	if _, err := legacy.ExecContext(ctx, "PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000"); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := legacy.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil || version != 3 {
		t.Fatalf("legacy opening guard sees version=%d error=%v", version, err)
	}
	// These are the original schema-v3 publisher's explicit-column statements.
	before := time.Now().UTC().Add(-time.Millisecond)
	tx, err := legacy.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"INSERT INTO catalog_generations(id,workspace_id,source_fingerprint,created_at) VALUES ('second','ws_test','source-second',1)",
		"INSERT INTO catalog_items(generation_id,id,revision,source_ref,execution_fingerprint,document_json) VALUES ('second','tool','1','source:tool','',CAST('{}' AS BLOB))",
		"INSERT INTO current_catalog(workspace_id,generation_id) VALUES ('ws_test','second') ON CONFLICT(workspace_id) DO UPDATE SET generation_id=excluded.generation_id",
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()
	var retired int64
	if err := r.db.QueryRowContext(ctx, "SELECT retired_at FROM catalog_retirements WHERE generation_id='first'").Scan(&retired); err != nil || retired < unixNano(before) || retired > unixNano(after) {
		t.Fatalf("legacy replacement retirement=%d error=%v", retired, err)
	}
	if count, err := r.DeleteRetiredCatalogGenerations(ctx, w.ID, after.Add(-time.Hour), after, 16); err != nil || count != 0 {
		t.Fatalf("legacy replacement bypassed grace: count=%d error=%v", count, err)
	}
	if _, err := legacy.ExecContext(ctx, "UPDATE current_catalog SET generation_id='first' WHERE workspace_id='ws_test'"); err != nil {
		t.Fatal(err)
	}
	var reactivated sql.NullInt64
	if err := r.db.QueryRowContext(ctx, "SELECT (SELECT retired_at FROM catalog_retirements WHERE generation_id='first')").Scan(&reactivated); err != nil || reactivated.Valid {
		t.Fatalf("legacy reactivation did not clear retirement: %+v error=%v", reactivated, err)
	}
	future := time.Now().UTC().Add(time.Hour + time.Second)
	if count, err := r.DeleteRetiredCatalogGenerations(ctx, w.ID, future.Add(-time.Hour), future, 16); err != nil || count != 1 {
		t.Fatalf("legacy-written retired generation cleanup: count=%d error=%v", count, err)
	}
	var id string
	if err := legacy.QueryRowContext(ctx, "SELECT id FROM catalog_items WHERE generation_id='first'").Scan(&id); err != nil || id != "tool" {
		t.Fatalf("legacy reader after cleanup: id=%q error=%v", id, err)
	}
}

func TestKnownRetirementV4RecoversV3WithoutLosingCatalogData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgraded.db")
	r, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	w := seedWorkspace(t, r)
	for _, id := range []string{"old", "current"} {
		if err := r.PublishCatalogGeneration(ctx, generation(w.ID, id, item("tool"))); err != nil {
			t.Fatal(err)
		}
	}
	// Recreate the precise pre-release v4 layout, including its observations.
	for _, statement := range []string{
		"ALTER TABLE catalog_generations ADD COLUMN retired_at INTEGER",
		"UPDATE catalog_generations SET retired_at=(SELECT retired_at FROM catalog_retirements r WHERE r.generation_id=catalog_generations.id)",
		"CREATE INDEX catalog_generations_retired ON catalog_generations(workspace_id,retired_at,id)",
		"DROP TRIGGER catalog_retention_activate",
		"DROP TRIGGER catalog_retention_replace",
		"DROP TRIGGER catalog_retention_remove",
		"DROP TABLE catalog_retirements",
		"INSERT INTO schema_migrations(version,applied_at) VALUES (4,1)",
	} {
		if _, err := r.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var version, columns int
	if err := r.db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil || version != 3 {
		t.Fatalf("recovered version=%d error=%v", version, err)
	}
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info('catalog_generations')").Scan(&columns); err != nil || columns != 4 {
		t.Fatalf("recovered column count=%d error=%v", columns, err)
	}
	var retired int64
	if err := r.db.QueryRowContext(ctx, "SELECT retired_at FROM catalog_retirements WHERE generation_id='old'").Scan(&retired); err != nil || retired != unixNano(w.CreatedAt) {
		t.Fatalf("retirement observation changed: %d error=%v", retired, err)
	}
	for _, id := range []string{"old", "current"} {
		if _, err := r.GetCatalogItem(ctx, w.ID, id, "tool"); err != nil {
			t.Fatalf("recovery lost %s: %v", id, err)
		}
	}
	if _, err := r.GetCatalogItem(ctx, w.ID, "current", "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("normal not-found behavior changed: %v", err)
	}
}
