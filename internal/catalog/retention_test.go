package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"repoplane/internal/store"
)

func TestRepeatedQueryReusesLiveResultSet(t *testing.T) {
	s, catalogPath := newServiceFixture(t, map[string]string{
		"a.yaml": "id: a.tool\nrevision: 1\nsummary: first\n",
		"b.yaml": "id: b.tool\nrevision: 1\nsummary: second\n",
	})
	ctx := context.Background()
	snapshot := func(request QueryRequest) string {
		t.Helper()
		response, err := s.Query(ctx, request)
		if err != nil || response.SnapshotRef == nil {
			t.Fatalf("query %+v: response=%+v error=%v", request, response, err)
		}
		return *response.SnapshotRef
	}
	list := snapshot(QueryRequest{Mode: "list", ItemLimit: 1})
	if again := snapshot(QueryRequest{Mode: "list", ItemLimit: 2}); again != list {
		t.Fatalf("repeated list wrote a new set: %s != %s", again, list)
	}
	search := snapshot(QueryRequest{Mode: "search", Query: "audit"})
	if search == list || snapshot(QueryRequest{Mode: "audit"}) == search {
		t.Fatal("distinct modes or queries shared a result set")
	}
	base := s.now()
	s.now = func() time.Time { return base.Add(resultSetTTL - resultSetMinReuse + time.Minute) }
	if stale := snapshot(QueryRequest{Mode: "list"}); stale == list {
		t.Fatal("reused a set without enough cursor lifetime left")
	}
	s.now = func() time.Time { return base }
	if err := os.WriteFile(filepath.Join(catalogPath, "b.yaml"), []byte("id: b.tool\nrevision: 2\nsummary: changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed := snapshot(QueryRequest{Mode: "list"}); changed == list {
		t.Fatal("reused a set across catalog generations")
	}
}

func TestUnchangedRefreshPrunesRetiredGenerationAndPreservesLiveCursor(t *testing.T) {
	s, catalogPath := newServiceFixture(t, map[string]string{
		"a.yaml": "id: a.tool\nrevision: 1\nsummary: first\n",
		"b.yaml": "id: b.tool\nrevision: 1\nsummary: second\n",
	})
	ctx := context.Background()
	first, err := s.Query(ctx, QueryRequest{Mode: "list", ItemLimit: 1})
	if err != nil || first.NextCursor == nil {
		t.Fatalf("first page=%+v error=%v", first, err)
	}
	old, err := s.repository.CurrentCatalogGeneration(ctx, s.workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalogPath, "b.yaml"), []byte("id: b.tool\nrevision: 2\nsummary: changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	current, err := s.indexer.Refresh(ctx)
	if err != nil || current.ID == old.ID {
		t.Fatalf("changed refresh=%+v error=%v", current, err)
	}
	// A source change and maintenance must not mix new results into an existing cursor.
	second, err := s.Query(ctx, QueryRequest{Cursor: *first.NextCursor, ItemLimit: 1})
	if err != nil || len(second.Items) != 1 || second.Items[0].Summary != "second" {
		t.Fatalf("live continuation=%+v error=%v", second, err)
	}
	future := current.CreatedAt.Add(retiredGenerationGrace + time.Minute)
	s.indexer.now = func() time.Time { return future }
	unchanged, err := s.indexer.Refresh(ctx)
	if err != nil || unchanged.ID != current.ID {
		t.Fatalf("unchanged refresh=%+v error=%v", unchanged, err)
	}
	if _, err := s.repository.GetCatalogItem(ctx, s.workspaceID, old.ID, "a.tool"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("retired generation survived unchanged refresh: %v", err)
	}
	if _, err := s.repository.GetCatalogItem(ctx, s.workspaceID, current.ID, "a.tool"); err != nil {
		t.Fatalf("current generation was pruned: %v", err)
	}
}
