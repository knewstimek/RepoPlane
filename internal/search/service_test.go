package search

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"repoplane/internal/cursor"
	"repoplane/internal/store"
	storesqlite "repoplane/internal/store/sqlite"
	"repoplane/internal/workspace"
)

type stubBackend struct {
	outcome BackendOutcome
	err     error
}

type deadlineBackend struct{}

func (deadlineBackend) Search(ctx context.Context, _ BackendQuery) (BackendOutcome, error) {
	<-ctx.Done()
	return BackendOutcome{
		Matches:  []Match{{Path: "observed.txt", Line: 1, Text: "observed"}},
		Complete: false, Warnings: []string{"search deadline or cancellation reached"}, Engine: "deadline-test",
	}, nil
}

func (b stubBackend) Search(context.Context, BackendQuery) (BackendOutcome, error) {
	return b.outcome, b.err
}

func newSearchService(t *testing.T, backend Backend) *Service {
	t.Helper()
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, "nested"), 0o700); err != nil {
		t.Fatal(err)
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
	service := NewService(root, repository, codec, backend)
	service.now = func() time.Time { return now }
	return service
}

func TestServicePaginationPreservesPartialMetadata(t *testing.T) {
	service := newSearchService(t, stubBackend{outcome: BackendOutcome{
		Matches:  []Match{{Path: "a.txt", Line: 1, Text: "one"}, {Path: "b.txt", Line: 2, Text: "two"}, {Path: "c.txt", Line: 3, Text: "three"}},
		Complete: false, Warnings: []string{"bounded scan stopped"}, Engine: "test-engine",
	}})
	first, err := service.Query(context.Background(), Request{Mode: "exact", Pattern: "text", ItemLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "partial" || first.Counts.Relation != "lower_bound" || first.NextCursor == nil || len(first.Items) != 2 {
		t.Fatalf("unexpected first page: %+v", first)
	}
	second, err := service.Query(context.Background(), Request{Cursor: *first.NextCursor, ItemLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "partial" || second.Counts.Relation != "lower_bound" || len(second.Items) != 1 || second.Truncated != nil {
		t.Fatalf("unexpected second page: %+v", second)
	}
	if first.SnapshotRef == nil || second.SnapshotRef == nil || *first.SnapshotRef != *second.SnapshotRef {
		t.Fatal("snapshot changed between pages")
	}
}

func TestServiceUnsupportedEncodingIsExplicit(t *testing.T) {
	service := newSearchService(t, stubBackend{})
	response, err := service.Query(context.Background(), Request{Mode: "exact", Pattern: "text", Encoding: "shift-jis"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "unsupported" || response.Counts.Matched != nil || response.Truncated != nil {
		t.Fatalf("unexpected unsupported response: %+v", response)
	}
}

func TestMissingRipgrepReportsUnsupportedWithoutMatches(t *testing.T) {
	service := newSearchService(t, UnavailableBackend{})
	response, err := service.Query(context.Background(), Request{Mode: "exact", Pattern: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "unsupported" || response.Scan.State != "not_applicable" || response.Counts.Matched != nil || response.Counts.Relation != "unknown" || len(response.Items) != 0 {
		t.Fatalf("unavailable search response=%+v", response)
	}
	if len(response.Warnings) != 1 || response.Warnings[0].Code != "ripgrep_unavailable" || !strings.Contains(response.Warnings[0].Message, "rg") {
		t.Fatalf("unavailable search warnings=%+v", response.Warnings)
	}
}

func TestServiceEnforcesByteLimit(t *testing.T) {
	service := newSearchService(t, stubBackend{outcome: BackendOutcome{
		Matches: []Match{{Path: "long.txt", Line: 1, Text: "a long result"}}, Complete: true, Engine: "test",
	}})
	_, err := service.Query(context.Background(), Request{Mode: "exact", Pattern: "text", ByteLimit: 64})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error=%v, want ErrResponseTooLarge", err)
	}
}

func TestServiceMakesSubrootResultsWorkspaceRelative(t *testing.T) {
	service := newSearchService(t, stubBackend{outcome: BackendOutcome{
		Matches: []Match{{Path: "file.txt", Line: 1, Text: "text"}}, Complete: true, Engine: "test",
	}})
	response, err := service.Query(context.Background(), Request{Mode: "exact", Pattern: "text", Root: "nested"})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || response.Items[0].Path != "nested/file.txt" || response.Items[0].SourceRef != "source:mutable:nested/file.txt" {
		t.Fatalf("unexpected workspace path: %+v", response.Items)
	}
}

func TestServiceDeadlinePreservesObservedLowerBound(t *testing.T) {
	service := newSearchService(t, deadlineBackend{})
	response, err := service.Query(context.Background(), Request{
		Mode: "exact", Pattern: "observed", TimeLimitMS: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "partial" || response.Scan.State != "partial" || response.Counts.Relation != "lower_bound" || len(response.Items) != 1 || len(response.Warnings) != 1 {
		t.Fatalf("deadline response=%+v", response)
	}
}
