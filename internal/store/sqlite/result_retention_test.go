package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"repoplane/internal/store"
)

func TestResultSetCreationPrunesExpiredSnapshotsAtomically(t *testing.T) {
	repository := openTestRepository(t)
	workspace := seedWorkspace(t, repository)
	ctx := context.Background()
	now := time.Unix(2_000_000_000, 0).UTC()
	makeSet := func(id string, created, expires time.Time) store.ResultSet {
		return store.ResultSet{
			ID: id, WorkspaceID: workspace.ID, QueryHash: "query", GenerationID: "search-v1",
			CreatedAt: created, ExpiresAt: expires, Metadata: json.RawMessage(`{"scan":"complete"}`),
			Items: []store.ResultItem{
				{Ordinal: 0, ItemRef: "item:0", ItemHash: "hash:0", Payload: json.RawMessage(`{"n":0}`)},
				{Ordinal: 1, ItemRef: "item:1", ItemHash: "hash:1", Payload: json.RawMessage(`{"n":1}`)},
			},
		}
	}
	past := now.Add(-time.Hour)
	for index := 0; index < expiredResultSetBatch+2; index++ {
		if err := repository.CreateResultSet(ctx, makeSet(fmt.Sprintf("expired-%03d", index), past, now)); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.CreateResultSet(ctx, makeSet("active", past, now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}

	// A failed snapshot creation must roll back maintenance as well.
	if err := repository.CreateResultSet(ctx, makeSet("active", now, now.Add(time.Hour))); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate creation error=%v, want ErrConflict", err)
	}
	var expired int
	if err := repository.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM result_sets WHERE expires_at<=?", unixNano(now)).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if expired != expiredResultSetBatch+2 {
		t.Fatalf("failed creation retained %d expired snapshots, want %d", expired, expiredResultSetBatch+2)
	}

	if err := repository.CreateResultSet(ctx, makeSet("new", now, now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := repository.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM result_sets WHERE expires_at<=?", unixNano(now)).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if expired != 2 {
		t.Fatalf("remaining expired snapshots=%d, want 2", expired)
	}
	var items int
	if err := repository.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM result_items").Scan(&items); err != nil {
		t.Fatal(err)
	}
	if items != 8 {
		t.Fatalf("remaining items=%d, want 8 (expired items must cascade)", items)
	}
	if _, err := repository.ReadResultPage(ctx, "expired-000", 0, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("oldest expired snapshot error=%v, want ErrNotFound", err)
	}
	page, err := repository.ReadResultPage(ctx, "active", 1, 1)
	if err != nil || len(page.Items) != 1 || string(page.Items[0].Payload) != `{"n":1}` {
		t.Fatalf("unexpired continuation changed: page=%+v error=%v", page, err)
	}
}
