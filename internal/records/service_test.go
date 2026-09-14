package records

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"repoplane/internal/contracts"
	"repoplane/internal/cursor"
	"repoplane/internal/store"
	storesqlite "repoplane/internal/store/sqlite"
	"repoplane/internal/workspace"
)

func testService(t *testing.T) (*Service, string) {
	t.Helper()
	rootPath := t.TempDir()
	runGit(t, rootPath, "init", "-q")
	if err := os.WriteFile(filepath.Join(rootPath, ".gitignore"), []byte("/.tmp/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, rootPath, "add", ".")
	runGit(t, rootPath, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "commit", "-qm", "fixture")
	root, err := workspace.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	cache, err := storesqlite.Open(context.Background(), filepath.Join(state, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	recordStore, err := storesqlite.OpenRecords(context.Background(), filepath.Join(state, "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := cache.UpsertWorkspace(context.Background(), store.Workspace{ID: root.ID(), RootFingerprint: root.ID(), CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.NewCodec(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close(); _ = recordStore.Close() })
	return NewService(root, recordStore, cache, codec), rootPath
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func writeChecklist(t *testing.T, root string) string {
	t.Helper()
	relative := "checks/test.yaml"
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte("schema_version: verification-check.v1\nid: test.verify\nrevision: 1\napplies_to:\n  paths: ['**']\nrequired: true\ncapability: dev.verify\nconfigurations: [default]\nsuccess:\n  report_required: true\n  minimum_executed_checks: 1\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return relative
}

func TestCheckpointOptimisticConcurrency(t *testing.T) {
	service, _ := testService(t)
	created, err := service.WriteCheckpoint(context.Background(), CheckpointRequest{Mode: "create", Goal: "finish records", RemainingChecks: []string{"tests"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Record.Payload["baseline_commit"] == "" || created.Record.Payload["dirty"] != false {
		t.Fatalf("automatic checkpoint baseline=%v", created.Record.Payload)
	}
	updated, err := service.WriteCheckpoint(context.Background(), CheckpointRequest{Mode: "update", ID: created.Record.ID, ExpectedRevision: 1, Goal: "finish records", NextAction: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Record.Revision != 2 {
		t.Fatalf("revision=%d", updated.Record.Revision)
	}
	_, err = service.WriteCheckpoint(context.Background(), CheckpointRequest{Mode: "update", ID: created.Record.ID, ExpectedRevision: 1, Goal: "overwrite"})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("error=%v, want conflict", err)
	}
}

func TestMemoKindsAndSupersede(t *testing.T) {
	service, _ := testService(t)
	created, err := service.WriteMemo(context.Background(), MemoRequest{Mode: "create", MemoKind: "decision", Content: "Use a separate durable database."})
	if err != nil {
		t.Fatal(err)
	}
	if created.Record.Source != "user_asserted" {
		t.Fatalf("source=%q", created.Record.Source)
	}
	superseded, err := service.WriteMemo(context.Background(), MemoRequest{Mode: "supersede", ID: created.Record.ID, ExpectedRevision: 1, Source: "user_asserted"})
	if err != nil {
		t.Fatal(err)
	}
	if superseded.Record.Validity != "superseded" {
		t.Fatalf("validity=%q", superseded.Record.Validity)
	}
}

func TestImportReportIsIdempotentAndBecomesStale(t *testing.T) {
	service, root := testService(t)
	commit := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	reportDir := filepath.Join(root, ".tmp")
	if err := os.MkdirAll(reportDir, 0o700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{
		"schema_version": "check-report.v1", "operation": "verify", "status": "passed",
		"started_at": time.Now().UTC(), "finished_at": time.Now().UTC(),
		"subject": map[string]any{"git_commit": commit, "dirty": false},
		"checks":  []map[string]any{{"id": "test.all", "status": "passed", "duration_ms": 1, "diagnostics": "sensitive-fixture-diagnostic"}},
		"counts":  map[string]int{"passed": 1, "failed": 0, "unknown": 0}, "truncated": false,
	}
	data, _ := json.Marshal(report)
	path := filepath.Join(reportDir, "verify.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	checklistPath := writeChecklist(t, root)
	runGit(t, root, "add", "checks/test.yaml")
	runGit(t, root, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "commit", "-qm", "checklist")
	// The report describes the prior commit, so regenerate it for the checklist commit.
	commit = strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	report["subject"] = map[string]any{"git_commit": commit, "dirty": false}
	data, _ = json.Marshal(report)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := service.ImportReport(context.Background(), ImportRequest{Path: ".tmp/verify.json", ChecklistPath: checklistPath, Configuration: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || first.Record.Validity != "current" {
		t.Fatalf("first=%+v", first)
	}
	encoded, _ := json.Marshal(first.Record.Payload)
	if strings.Contains(string(encoded), "sensitive-fixture-diagnostic") {
		t.Fatal("imported record retained raw report diagnostics")
	}
	if first.Record.Payload["outcome"] != "passed" || first.Record.Payload["raw_storage"] != "hash_only" {
		t.Fatalf("verification payload=%v", first.Record.Payload)
	}
	second, err := service.ImportReport(context.Background(), ImportRequest{Path: ".tmp/verify.json", ChecklistPath: checklistPath, Configuration: "default"})
	if err != nil || !second.Duplicate || second.Record.ID != first.Record.ID {
		t.Fatalf("second=%+v error=%v", second, err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	query, err := service.Query(context.Background(), QueryRequest{Mode: "get", ID: first.Record.ID})
	if err != nil {
		t.Fatal(err)
	}
	if query.Items[0].Validity != "stale" {
		t.Fatalf("validity=%q", query.Items[0].Validity)
	}
	report["subject"] = map[string]any{"git_commit": commit, "dirty": true}
	dirtyData, _ := json.Marshal(report)
	if err := os.WriteFile(filepath.Join(reportDir, "dirty.json"), dirtyData, 0o600); err != nil {
		t.Fatal(err)
	}
	dirtyImport, err := service.ImportReport(context.Background(), ImportRequest{Path: ".tmp/dirty.json", ChecklistPath: checklistPath, Configuration: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if dirtyImport.Record.Validity != "unknown" {
		t.Fatalf("dirty validity=%q", dirtyImport.Record.Validity)
	}
}

func TestImportRejectsInconsistentAndEscapingReports(t *testing.T) {
	service, root := testService(t)
	checklistPath := writeChecklist(t, root)
	reportDir := filepath.Join(root, ".tmp")
	if err := os.MkdirAll(reportDir, 0o700); err != nil {
		t.Fatal(err)
	}
	invalid := `{"schema_version":"check-report.v1","operation":"verify","status":"passed","started_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:01Z","subject":{},"checks":[],"counts":{"passed":0,"failed":0,"unknown":0},"truncated":false}`
	if err := os.WriteFile(filepath.Join(reportDir, "invalid.json"), []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportReport(context.Background(), ImportRequest{Path: ".tmp/invalid.json", ChecklistPath: checklistPath, Configuration: "default"}); !errors.Is(err, ErrReportInvalid) {
		t.Fatalf("invalid report error=%v", err)
	}
	if _, err := service.ImportReport(context.Background(), ImportRequest{Path: "../outside.json", ChecklistPath: checklistPath, Configuration: "default"}); err == nil {
		t.Fatal("import accepted workspace escape")
	}
	if _, err := service.ImportReport(context.Background(), ImportRequest{Path: ".tmp/missing.json", ChecklistPath: checklistPath, Configuration: "default"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing report error=%v", err)
	}
	skipped := `{"schema_version":"check-report.v1","operation":"verify","status":"partial","started_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:01Z","subject":{},"checks":[{"id":"test","status":"skipped"}],"counts":{"passed":0,"failed":0,"unknown":1},"truncated":false}`
	if err := os.WriteFile(filepath.Join(reportDir, "skipped.json"), []byte(skipped), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportReport(context.Background(), ImportRequest{Path: ".tmp/skipped.json", ChecklistPath: checklistPath, Configuration: "default"}); !errors.Is(err, ErrReportInvalid) {
		t.Fatalf("skipped report error=%v", err)
	}
}

func TestImportRejectsOversizedReport(t *testing.T) {
	service, root := testService(t)
	checklistPath := writeChecklist(t, root)
	reportDir := filepath.Join(root, ".tmp")
	if err := os.MkdirAll(reportDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reportDir, "large.json"), make([]byte, maximumReportBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportReport(context.Background(), ImportRequest{Path: ".tmp/large.json", ChecklistPath: checklistPath, Configuration: "default"}); !errors.Is(err, contracts.ErrLimitExceeded) {
		t.Fatalf("oversized report error=%v", err)
	}
}

func TestRecordsPaginationUsesFixedSnapshot(t *testing.T) {
	service, _ := testService(t)
	for _, content := range []string{"one", "two", "three"} {
		if _, err := service.WriteMemo(context.Background(), MemoRequest{Mode: "create", MemoKind: "limitation", Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := service.Query(context.Background(), QueryRequest{Mode: "list", ItemLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.NextCursor == nil || first.Counts.Matched == nil || *first.Counts.Matched != 3 {
		t.Fatalf("first=%+v", first)
	}
	if _, err := service.WriteMemo(context.Background(), MemoRequest{Mode: "create", MemoKind: "limitation", Content: "later"}); err != nil {
		t.Fatal(err)
	}
	second, err := service.Query(context.Background(), QueryRequest{Cursor: *first.NextCursor, ItemLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Counts.Matched == nil || *second.Counts.Matched != 3 {
		t.Fatalf("second=%+v", second)
	}
}

func TestRecordsUpdatedAfterFilter(t *testing.T) {
	service, _ := testService(t)
	if _, err := service.WriteMemo(context.Background(), MemoRequest{Mode: "create", MemoKind: "limitation", Content: "bounded"}); err != nil {
		t.Fatal(err)
	}
	response, err := service.Query(context.Background(), QueryRequest{Mode: "list", UpdatedAfter: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if response.Counts.Matched == nil || *response.Counts.Matched != 0 || len(response.Items) != 0 {
		t.Fatalf("response=%+v", response)
	}
}
