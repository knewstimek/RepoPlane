package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"repoplane/internal/cursor"
	"repoplane/internal/store"
	storesqlite "repoplane/internal/store/sqlite"
	"repoplane/internal/workspace"
)

func newServiceFixture(t *testing.T, manifests map[string]string) (*Service, string) {
	t.Helper()
	rootPath := t.TempDir()
	catalogPath := filepath.Join(rootPath, "catalog")
	if err := os.Mkdir(catalogPath, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range manifests {
		if err := os.WriteFile(filepath.Join(catalogPath, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := workspace.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	now := time.Unix(2_000_000_000, 0).UTC()
	if err := repository.UpsertWorkspace(context.Background(), store.Workspace{
		ID: root.ID(), RootFingerprint: root.ID(), CreatedAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.NewCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	indexer := NewIndexer(root, repository, []string{"catalog"})
	indexer.now = func() time.Time { return now }
	service := NewService(root.ID(), repository, indexer, codec)
	service.now = func() time.Time { return now }
	return service, catalogPath
}

func TestServicePaginatesFixedCatalogResults(t *testing.T) {
	service, catalogPath := newServiceFixture(t, map[string]string{
		"a.yaml": "id: a.tool\nrevision: 1\nsummary: test alpha\n",
		"b.yaml": "id: b.tool\nrevision: 1\nsummary: test beta\n",
		"c.yaml": "id: c.tool\nrevision: 1\nsummary: test gamma\n",
	})
	first, err := service.Query(context.Background(), QueryRequest{Mode: "search", Query: "test", ItemLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Counts.Matched == nil || *first.Counts.Matched != 3 || first.NextCursor == nil || !*first.Truncated {
		t.Fatalf("unexpected first page: %+v", first)
	}
	// A later source edit must not mix current catalog data into the fixed set.
	if err := os.WriteFile(filepath.Join(catalogPath, "c.yaml"), []byte(
		"id: c.tool\nrevision: 2\nsummary: changed after first page\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := service.Query(context.Background(), QueryRequest{Cursor: *first.NextCursor, ItemLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].Summary != "test gamma" || second.NextCursor != nil || *second.Truncated {
		t.Fatalf("unexpected second page: %+v", second)
	}
	if first.SnapshotRef == nil || second.SnapshotRef == nil || *first.SnapshotRef != *second.SnapshotRef {
		t.Fatalf("snapshot changed between pages: %v %v", first.SnapshotRef, second.SnapshotRef)
	}
}

func TestServiceRanksExactIDFirstAndGetReturnsManifest(t *testing.T) {
	service, _ := newServiceFixture(t, map[string]string{
		"exact.yaml": "id: schema.validate\nrevision: 1\nsummary: exact item\n",
		"other.yaml": "id: other.tool\nrevision: 1\nsummary: schema validate helper\n",
	})
	search, err := service.Query(context.Background(), QueryRequest{Mode: "search", Query: "schema.validate"})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Items) == 0 || search.Items[0].ID != "schema.validate" || search.Items[0].MatchReason != "exact_id" {
		t.Fatalf("exact ID was not first: %+v", search.Items)
	}
	get, err := service.Query(context.Background(), QueryRequest{Mode: "get", ID: "schema.validate"})
	if err != nil {
		t.Fatal(err)
	}
	if len(get.Items) != 1 || get.Items[0].Manifest == nil || get.Items[0].Manifest.ID != "schema.validate" {
		t.Fatalf("unexpected get response: %+v", get)
	}
}

func TestServiceAuditReportsManifestFailures(t *testing.T) {
	service, _ := newServiceFixture(t, map[string]string{
		"one.yaml":     "id: duplicate.tool\nrevision: 1\nsummary: one\n",
		"two.yaml":     "id: duplicate.tool\nrevision: 1\nsummary: two\n",
		"invalid.yaml": "id: INVALID\nrevision: 1\nsummary: invalid\n",
	})
	audit, err := service.Query(context.Background(), QueryRequest{Mode: "audit"})
	if err != nil {
		t.Fatal(err)
	}
	if len(audit.Items) != 3 || audit.Counts.Matched == nil || *audit.Counts.Matched != 3 {
		t.Fatalf("unexpected audit response: %+v", audit)
	}
}

func TestServiceEnforcesResponseByteLimit(t *testing.T) {
	service, _ := newServiceFixture(t, map[string]string{
		"tool.yaml": "id: test.tool\nrevision: 1\nsummary: a response that cannot fit a tiny envelope budget\n",
	})
	_, err := service.Query(context.Background(), QueryRequest{
		Mode: "list", ItemLimit: 1, ByteLimit: 64,
	})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error=%v, want ErrResponseTooLarge", err)
	}
}
