package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"repoplane/internal/store"
)

func TestUsageAggregatesByDayWorkspaceAndOutcome(t *testing.T) {
	ctx := context.Background()
	db, err := OpenUsage(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for _, event := range []store.UsageEvent{
		{At: at, WorkspaceID: "a", Transport: "stdio", Tool: "catalog_query", Outcome: "ok", RequestBytes: 10, ResponseBytes: 20, DurationNS: uint64(2 * time.Millisecond)},
		{At: at, WorkspaceID: "a", Transport: "stdio", Tool: "catalog_query", Outcome: "ok", RequestBytes: 11, ResponseBytes: 21, DurationNS: uint64(3 * time.Millisecond)},
		{At: at, WorkspaceID: "a", Transport: "stdio", Tool: "catalog_query", Outcome: "error", RequestBytes: 12, DurationNS: uint64(time.Millisecond)},
		{At: at, WorkspaceID: "b", Transport: "stdio", Tool: "catalog_query", Outcome: "ok", RequestBytes: 99},
	} {
		if err := db.RecordUsage(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	rows, first, err := db.QueryUsage(ctx, "a", "2026-09-16", "2026-09-16")
	if err != nil {
		t.Fatal(err)
	}
	if first != "2026-09-16" || len(rows) != 2 || rows[1].Outcome != "ok" || rows[1].Calls != 2 || rows[1].RequestBytes != 21 || rows[1].ResponseBytes != 41 || rows[1].DurationMS != 5 {
		t.Fatalf("first=%q rows=%+v", first, rows)
	}
}
