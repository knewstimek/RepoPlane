package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"repoplane/internal/store"
)

func TestRetiredCatalogCleanupProtectsReferencesAndWorkspace(t *testing.T) {
	r := openTestRepository(t)
	w := seedWorkspace(t, r)
	ctx := context.Background()
	base := w.CreatedAt
	other := store.Workspace{ID: "ws_other", RootFingerprint: "other", CreatedAt: base, LastSeenAt: base}
	if err := r.UpsertWorkspace(ctx, other); err != nil {
		t.Fatal(err)
	}
	publish := func(workspaceID, id string, at time.Time) {
		t.Helper()
		g := generation(workspaceID, id, item("tool", store.CatalogTerm{Field: "id", Term: "tool", Weight: 1}))
		g.Meta.CreatedAt = at
		g.Issues = []store.CatalogIssue{{Code: "test", SourceRef: "source:test", Detail: json.RawMessage(`{}`)}}
		if err := r.PublishCatalogGeneration(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	publish(other.ID, "other-old", base)
	publish(other.ID, "other-current", base)
	for _, id := range []string{"old-0", "old-1-live", "old-2-expired", "long-current"} {
		publish(w.ID, id, base)
	}
	for _, snapshot := range []struct {
		id, generation string
		expires        time.Time
	}{
		{"live-page", "old-1-live", base.Add(3 * time.Hour)},
		{"expired-page", "old-2-expired", base.Add(30 * time.Minute)},
	} {
		if err := r.CreateResultSet(ctx, store.ResultSet{
			ID: snapshot.id, WorkspaceID: w.ID, QueryHash: "query", GenerationID: snapshot.generation,
			CreatedAt: base, ExpiresAt: snapshot.expires, Metadata: json.RawMessage(`{}`),
			Items: []store.ResultItem{{Ordinal: 0, ItemRef: "tool", ItemHash: "hash", Payload: json.RawMessage(`{"id":"tool"}`)}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	now := base.Add(2 * time.Hour)
	publish(w.ID, "current", now) // An old creation date must not bypass retirement grace.
	deleted, err := r.DeleteRetiredCatalogGenerations(ctx, w.ID, now.Add(-time.Hour), now, 1)
	if err != nil || deleted != 1 {
		t.Fatalf("first batch deleted=%d error=%v, want 1", deleted, err)
	}
	if _, err := r.GetCatalogItem(ctx, w.ID, "old-0", "tool"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("oldest unreferenced generation error=%v, want ErrNotFound", err)
	}
	for _, table := range []string{"catalog_items", "catalog_terms", "catalog_issues"} {
		var count int
		if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE generation_id='old-0'").Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s cascade count=%d error=%v", table, count, err)
		}
	}
	deleted, err = r.DeleteRetiredCatalogGenerations(ctx, w.ID, now.Add(-time.Hour), now, 16)
	if err != nil || deleted != 1 {
		t.Fatalf("second batch deleted=%d error=%v, want expired-reference generation only", deleted, err)
	}
	for _, id := range []string{"old-1-live", "long-current", "current"} {
		if _, err := r.GetCatalogItem(ctx, w.ID, id, "tool"); err != nil {
			t.Fatalf("protected generation %s: %v", id, err)
		}
	}
	if _, err := r.GetCatalogItem(ctx, other.ID, "other-old", "tool"); err != nil {
		t.Fatalf("unrelated workspace was pruned: %v", err)
	}
	page, err := r.ReadResultPage(ctx, "live-page", 0, 1)
	if err != nil || len(page.Items) != 1 || string(page.Items[0].Payload) != `{"id":"tool"}` {
		t.Fatalf("live pagination changed: %+v error=%v", page, err)
	}
	now = base.Add(3 * time.Hour) // Equality at expiry releases the reference.
	deleted, err = r.DeleteRetiredCatalogGenerations(ctx, w.ID, now.Add(-time.Hour), now, 16)
	if err != nil || deleted != 2 {
		t.Fatalf("after expiry deleted=%d error=%v, want 2", deleted, err)
	}
	current, err := r.CurrentCatalogGeneration(ctx, w.ID)
	if err != nil || current.ID != "current" {
		t.Fatalf("current generation changed: %+v error=%v", current, err)
	}
}

func TestReactivatedCatalogGenerationGetsFreshRetirementGrace(t *testing.T) {
	r := openTestRepository(t)
	w := seedWorkspace(t, r)
	ctx := context.Background()
	first := generation(w.ID, "first", item("tool"))
	second := generation(w.ID, "second", item("tool"))
	for _, g := range []store.CatalogGeneration{first, second} {
		if err := r.PublishCatalogGeneration(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	now := w.CreatedAt.Add(2 * time.Hour)
	first.Meta.CreatedAt = now
	if err := r.PublishCatalogGeneration(ctx, first); err != nil {
		t.Fatal(err)
	}
	var retired sql.NullInt64
	if err := r.db.QueryRowContext(ctx, "SELECT retired_at FROM catalog_generations WHERE id='first'").Scan(&retired); err != nil || retired.Valid {
		t.Fatalf("reactivated generation retirement=%+v error=%v", retired, err)
	}
	if deleted, err := r.DeleteRetiredCatalogGenerations(ctx, w.ID, now.Add(-time.Hour), now, 16); err != nil || deleted != 0 {
		t.Fatalf("recently retired generation deleted=%d error=%v", deleted, err)
	}
	now = now.Add(time.Hour)
	if deleted, err := r.DeleteRetiredCatalogGenerations(ctx, w.ID, now.Add(-time.Hour), now, 16); err != nil || deleted != 1 {
		t.Fatalf("after grace deleted=%d error=%v, want 1", deleted, err)
	}
	second.Meta.CreatedAt = now
	if err := r.PublishCatalogGeneration(ctx, second); err != nil {
		t.Fatalf("rebuild previously pruned deterministic generation: %v", err)
	}
	if _, err := r.GetCatalogItem(ctx, w.ID, second.Meta.ID, "tool"); err != nil {
		t.Fatalf("rebuilt generation unreadable: %v", err)
	}
}

func TestCatalogRetirementMigrationStartsGraceWithoutLosingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	for _, migrate := range []func(context.Context, *sql.Tx) error{migrateV1, migrateV2, migrateV3} {
		if err := migrate(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		"INSERT INTO schema_migrations VALUES (3, 1)",
		"INSERT INTO workspaces VALUES ('ws', 'root', 1, 1)",
		"INSERT INTO catalog_generations VALUES ('old', 'ws', 'old-hash', 1), ('current', 'ws', 'current-hash', 2)",
		"INSERT INTO current_catalog VALUES ('ws', 'current')",
		"INSERT INTO catalog_items(generation_id,id,revision,source_ref,document_json,execution_fingerprint) VALUES ('old','tool','1','source:tool',CAST('{}' AS BLOB),'')",
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	r, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	after := time.Now().UTC()
	var retirement int64
	if err := r.db.QueryRowContext(ctx, "SELECT retired_at FROM catalog_generations WHERE id='old'").Scan(&retirement); err != nil {
		t.Fatal(err)
	}
	if retirement < unixNano(before) || retirement > unixNano(after) {
		t.Fatal("legacy retirement did not start a fresh migration grace period")
	}
	var currentRetirement sql.NullInt64
	if err := r.db.QueryRowContext(ctx, "SELECT retired_at FROM catalog_generations WHERE id='current'").Scan(&currentRetirement); err != nil || currentRetirement.Valid {
		t.Fatalf("legacy current marked retired: %+v error=%v", currentRetirement, err)
	}
	if _, err := r.GetCatalogItem(ctx, "ws", "old", "tool"); err != nil {
		t.Fatalf("migration lost legacy item: %v", err)
	}
	if deleted, err := r.DeleteRetiredCatalogGenerations(ctx, "ws", after.Add(-time.Hour), after, 16); err != nil || deleted != 0 {
		t.Fatalf("migration immediately pruned legacy data: deleted=%d error=%v", deleted, err)
	}
	if err := r.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	var unchanged int64
	if err := r.db.QueryRowContext(ctx, "SELECT retired_at FROM catalog_generations WHERE id='old'").Scan(&unchanged); err != nil || unchanged != retirement {
		t.Fatalf("reopening reset retirement: before=%d after=%d error=%v", retirement, unchanged, err)
	}
}
