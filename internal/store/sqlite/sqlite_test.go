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

func TestOpenMigratesPreReleaseResultSetShape(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`,
		`CREATE TABLE result_sets (
            id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, query_hash TEXT NOT NULL,
            generation_id TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL)`,
		`CREATE TABLE result_items (
            result_set_id TEXT NOT NULL, ordinal INTEGER NOT NULL, item_ref TEXT NOT NULL,
            item_hash TEXT NOT NULL, payload_json BLOB NOT NULL, PRIMARY KEY(result_set_id, ordinal))`,
		`INSERT INTO result_sets VALUES ('set-1', 'workspace-1', 'query', 'generation', 1, 2)`,
		`INSERT INTO result_items VALUES ('set-1', 0, 'ref-1', 'hash-1', '{}')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	var version, itemCount int
	var metadata string
	if err := repository.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := repository.db.QueryRowContext(ctx, `SELECT item_count, metadata_json FROM result_sets WHERE id='set-1'`).Scan(&itemCount, &metadata); err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion || itemCount != 1 || metadata != "{}" {
		t.Fatalf("version=%d item_count=%d metadata=%q", version, itemCount, metadata)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations VALUES (?, 0)`, currentSchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if repository, err := Open(ctx, path); err == nil {
		_ = repository.Close()
		t.Fatal("Open succeeded with a newer schema")
	}
}

func openTestRepository(t *testing.T) *Repository {
	t.Helper()
	repository, err := Open(context.Background(), filepath.Join(t.TempDir(), "repoplane.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("close repository: %v", err)
		}
	})
	return repository
}

func seedWorkspace(t *testing.T, repository *Repository) store.Workspace {
	t.Helper()
	now := time.Unix(2_000_000_000, 0).UTC()
	workspace := store.Workspace{ID: "ws_test", RootFingerprint: "root-hash", CreatedAt: now, LastSeenAt: now}
	if err := repository.UpsertWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	return workspace
}

func generation(workspaceID, id string, items ...store.CatalogItem) store.CatalogGeneration {
	return store.CatalogGeneration{
		Meta: store.CatalogGenerationMeta{
			ID: id, WorkspaceID: workspaceID, SourceFingerprint: "source-" + id,
			CreatedAt: time.Unix(2_000_000_000, 0).UTC(),
		},
		Items: items,
	}
}

func item(id string, terms ...store.CatalogTerm) store.CatalogItem {
	document, _ := json.Marshal(map[string]any{"id": id})
	return store.CatalogItem{ID: id, Revision: "1", SourceRef: "source:" + id, Document: document, Terms: terms}
}

func TestWorkspaceRoundTrip(t *testing.T) {
	repository := openTestRepository(t)
	want := seedWorkspace(t, repository)
	got, err := repository.GetWorkspace(context.Background(), want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.RootFingerprint != want.RootFingerprint || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestCatalogPublishIsAtomicAndSearchIsDeterministic(t *testing.T) {
	repository := openTestRepository(t)
	workspace := seedWorkspace(t, repository)
	first := generation(workspace.ID, "generation-1",
		item("schema.validate", store.CatalogTerm{Field: "id", Term: "schema.validate", Weight: 100}),
	)
	if err := repository.PublishCatalogGeneration(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	invalid := generation(workspace.ID, "generation-2", item("duplicate"), item("duplicate"))
	if err := repository.PublishCatalogGeneration(context.Background(), invalid); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("invalid publish error=%v, want ErrConflict", err)
	}
	current, err := repository.CurrentCatalogGeneration(context.Background(), workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != first.Meta.ID {
		t.Fatalf("current generation=%q, want %q", current.ID, first.Meta.ID)
	}

	second := generation(workspace.ID, "generation-3",
		item("z-tool", store.CatalogTerm{Field: "summary", Term: "validate data", Weight: 10}),
		item("schema.validate", store.CatalogTerm{Field: "summary", Term: "validate schema", Weight: 10}),
		item("a-tool", store.CatalogTerm{Field: "summary", Term: "validate config", Weight: 10}),
	)
	if err := repository.PublishCatalogGeneration(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	page, err := repository.SearchCatalog(context.Background(), store.CatalogQuery{
		WorkspaceID: workspace.ID, ExactID: "schema.validate", Terms: []string{"validate"}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"schema.validate", "a-tool", "z-tool"}
	if len(page.Matches) != len(wantOrder) || page.Matched == nil || *page.Matched != 3 || !page.Complete {
		t.Fatalf("unexpected page metadata: %+v", page)
	}
	for index, want := range wantOrder {
		if page.Matches[index].Item.ID != want {
			t.Fatalf("match %d=%q, want %q", index, page.Matches[index].Item.ID, want)
		}
	}
}

func TestResultSetPaginationAndBoundedExpiry(t *testing.T) {
	repository := openTestRepository(t)
	workspace := seedWorkspace(t, repository)
	if err := repository.PublishCatalogGeneration(context.Background(), generation(workspace.ID, "generation", item("tool"))); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000_000_000, 0).UTC()
	makeSet := func(id string, expires time.Time) store.ResultSet {
		return store.ResultSet{
			ID: id, WorkspaceID: workspace.ID, QueryHash: "query", GenerationID: "generation",
			CreatedAt: now, ExpiresAt: expires, Metadata: json.RawMessage(`{"scan":"complete"}`),
			Items: []store.ResultItem{
				{Ordinal: 0, ItemRef: "item:0", ItemHash: "hash:0", Payload: json.RawMessage(`{"n":0}`)},
				{Ordinal: 1, ItemRef: "item:1", ItemHash: "hash:1", Payload: json.RawMessage(`{"n":1}`)},
				{Ordinal: 2, ItemRef: "item:2", ItemHash: "hash:2", Payload: json.RawMessage(`{"n":2}`)},
			},
		}
	}
	if err := repository.CreateResultSet(context.Background(), makeSet("active", now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	page, err := repository.ReadResultPage(context.Background(), "active", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.NextOrdinal == nil || *page.NextOrdinal != 2 {
		t.Fatalf("unexpected first page: %+v", page)
	}
	for _, id := range []string{"expired-1", "expired-2"} {
		if err := repository.CreateResultSet(context.Background(), makeSet(id, now.Add(time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := repository.DeleteExpiredResultSets(context.Background(), now.Add(2*time.Minute), 1)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d, want 1", deleted)
	}
}

func TestResultSetRejectsOrdinalGap(t *testing.T) {
	repository := openTestRepository(t)
	workspace := seedWorkspace(t, repository)
	set := store.ResultSet{
		ID: "bad", WorkspaceID: workspace.ID, QueryHash: "query", GenerationID: "generation",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), Metadata: json.RawMessage(`{}`),
		Items: []store.ResultItem{{Ordinal: 1, ItemRef: "item", ItemHash: "hash", Payload: json.RawMessage(`{}`)}},
	}
	if err := repository.CreateResultSet(context.Background(), set); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("error=%v, want ErrConflict", err)
	}
}
