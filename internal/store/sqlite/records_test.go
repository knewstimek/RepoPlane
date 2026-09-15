package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"repoplane/internal/store"
)

func openRecordRepository(t *testing.T) *RecordRepository {
	t.Helper()
	repository, err := OpenRecords(context.Background(), filepath.Join(t.TempDir(), "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	return repository
}

func testRecord(kind, id string) store.Record {
	now := time.Unix(2_000_000_000, 0).UTC()
	return store.Record{ID: id, Kind: kind, SchemaVersion: kind + ".v1", ProjectID: "project", WorkspaceID: "workspace", Revision: 1, Source: "user_asserted", WriterClass: "intention", Validity: "current", CreatedAt: now, UpdatedAt: now, Payload: json.RawMessage(`{"value":"one"}`), EvidenceRefs: []string{}}
}

func TestRecordRepositoryCheckpointCASAndHistory(t *testing.T) {
	repository := openRecordRepository(t)
	created, err := repository.CreateCheckpoint(context.Background(), store.RecordCreate{Record: testRecord("checkpoint", "checkpoint_1")})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := repository.UpdateCheckpoint(context.Background(), store.RecordUpdate{ProjectID: "project", WorkspaceID: "workspace", ID: created.ID, ExpectedRevision: 1, Payload: json.RawMessage(`{"value":"two"}`), EvidenceRefs: []string{"report:1"}, Validity: "current"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || string(updated.Payload) != `{"value":"two"}` {
		t.Fatalf("updated=%+v", updated)
	}
	_, err = repository.UpdateCheckpoint(context.Background(), store.RecordUpdate{ProjectID: "project", WorkspaceID: "workspace", ID: created.ID, ExpectedRevision: 1, Payload: json.RawMessage(`{"value":"lost"}`), Validity: "current"})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	var revisions int
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM record_revisions WHERE record_id=?`, created.ID).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 2 {
		t.Fatalf("revision rows=%d, want 2", revisions)
	}
}

func TestRecordRepositoryConcurrentCASAllowsOneWriter(t *testing.T) {
	repository := openRecordRepository(t)
	created, err := repository.CreateCheckpoint(context.Background(), store.RecordCreate{Record: testRecord("checkpoint", "checkpoint_race")})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, value := range []string{"two", "three"} {
		go func(value string) {
			<-start
			_, err := repository.UpdateCheckpoint(context.Background(), store.RecordUpdate{ProjectID: created.ProjectID, WorkspaceID: created.WorkspaceID, ID: created.ID, ExpectedRevision: 1, Payload: json.RawMessage(`{"value":"` + value + `"}`), Validity: "current"})
			results <- err
		}(value)
	}
	close(start)
	succeeded, conflicted := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, store.ErrConflict):
			conflicted++
		default:
			t.Fatalf("unexpected update error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestRecordRepositoryImportIsIdempotent(t *testing.T) {
	repository := openRecordRepository(t)
	record := testRecord("verification", "verification_1")
	record.Source = "imported"
	record.WriterClass = "importer"
	first, duplicate, err := repository.ImportVerification(context.Background(), store.RecordCreate{Record: record}, "sha256:report", "parser.1")
	if err != nil || duplicate {
		t.Fatalf("first import record=%+v duplicate=%v error=%v", first, duplicate, err)
	}
	record.ID = "verification_2"
	second, duplicate, err := repository.ImportVerification(context.Background(), store.RecordCreate{Record: record}, "sha256:report", "parser.1")
	if err != nil || !duplicate || second.ID != first.ID {
		t.Fatalf("second import record=%+v duplicate=%v error=%v", second, duplicate, err)
	}
}

func TestRecordRepositoryTransferPreservesHistoryAndRebindsWorkspace(t *testing.T) {
	source := openRecordRepository(t)
	record := testRecord("checkpoint", "checkpoint_transfer")
	created, err := source.CreateCheckpoint(context.Background(), store.RecordCreate{Record: record})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.UpdateCheckpoint(context.Background(), store.RecordUpdate{ProjectID: "project", WorkspaceID: "workspace", ID: created.ID, ExpectedRevision: 1, Payload: json.RawMessage(`{"value":"two"}`), EvidenceRefs: []string{"record:evidence"}, Validity: "current"}); err != nil {
		t.Fatal(err)
	}
	archive, err := source.ExportRecords(context.Background(), "project", "workspace", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Records) != 1 || len(archive.Revisions) != 2 {
		t.Fatalf("archive records=%d revisions=%d", len(archive.Records), len(archive.Revisions))
	}
	target := openRecordRepository(t)
	if err := target.RestoreRecords(context.Background(), "new-project", "new-workspace", archive); err != nil {
		t.Fatal(err)
	}
	restored, err := target.GetRecord(context.Background(), "new-project", "new-workspace", created.ID)
	if err != nil || restored.Revision != 2 || string(restored.Payload) != `{"value":"two"}` {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
	var revisions int
	if err := target.db.QueryRow(`SELECT COUNT(*) FROM record_revisions WHERE record_id=?`, created.ID).Scan(&revisions); err != nil || revisions != 2 {
		t.Fatalf("revision count=%d err=%v", revisions, err)
	}
	if err := target.RestoreRecords(context.Background(), "new-project", "new-workspace", archive); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("non-empty restore error=%v", err)
	}
}

func TestRecordRepositoryObservationWriterIsKindScoped(t *testing.T) {
	repository := openRecordRepository(t)
	record := testRecord("run", "run_00000000000000000000000000000001")
	record.Source = "observed"
	record.WriterClass = "server"
	created, err := repository.CreateObservation(context.Background(), store.RecordCreate{Record: record})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := repository.UpdateObservation(context.Background(), "run", store.RecordUpdate{
		ProjectID: created.ProjectID, WorkspaceID: created.WorkspaceID, ID: created.ID,
		ExpectedRevision: 1, Payload: json.RawMessage(`{"value":"terminal"}`), EvidenceRefs: []string{}, Validity: "current",
	})
	if err != nil || updated.Revision != 2 {
		t.Fatalf("observation update=%+v err=%v", updated, err)
	}
	for _, kind := range []string{"checkpoint", "verification", "custom"} {
		invalid := testRecord(kind, kind+"_server")
		invalid.Source = "observed"
		invalid.WriterClass = "server"
		if _, err := repository.CreateObservation(context.Background(), store.RecordCreate{Record: invalid}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("server observation accepted kind %q: %v", kind, err)
		}
	}
}

func TestRecordRepositoryScopesReadsToWorkspace(t *testing.T) {
	repository := openRecordRepository(t)
	created, err := repository.CreateMemo(context.Background(), store.RecordCreate{Record: testRecord("memo", "memo_1")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetRecord(context.Background(), created.ProjectID, "other-workspace", created.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-workspace read error=%v", err)
	}
	page, err := repository.QueryRecords(context.Background(), store.RecordQuery{ProjectID: created.ProjectID, WorkspaceID: created.WorkspaceID, Limit: 10})
	if err != nil || page.Matched != 1 || len(page.Records) != 1 {
		t.Fatalf("page=%+v error=%v", page, err)
	}
}

func TestRecordRepositoryLexicalTermsFilterAndRank(t *testing.T) {
	repository := openRecordRepository(t)
	older := testRecord("memo", "memo_payload")
	older.Payload = json.RawMessage(`{"scope":"docs","content":"Windows executable replacement"}`)
	older.UpdatedAt = older.UpdatedAt.Add(-time.Hour)
	if _, err := repository.CreateMemo(context.Background(), store.RecordCreate{Record: older}); err != nil {
		t.Fatal(err)
	}
	newer := testRecord("memo", "memo_windows")
	newer.Payload = json.RawMessage(`{"scope":"other","content":"unrelated"}`)
	if _, err := repository.CreateMemo(context.Background(), store.RecordCreate{Record: newer}); err != nil {
		t.Fatal(err)
	}
	miss := testRecord("memo", "memo_miss")
	miss.Payload = json.RawMessage(`{"content":"network timeout"}`)
	if _, err := repository.CreateMemo(context.Background(), store.RecordCreate{Record: miss}); err != nil {
		t.Fatal(err)
	}
	page, err := repository.QueryRecords(context.Background(), store.RecordQuery{ProjectID: "project", WorkspaceID: "workspace", Kind: "memo", Terms: []string{"windows", "executable"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Matched != 2 || len(page.Records) != 2 || page.Records[0].ID != "memo_windows" || page.Records[1].ID != "memo_payload" {
		t.Fatalf("search page=%+v", page)
	}
	keys, err := repository.QueryRecords(context.Background(), store.RecordQuery{ProjectID: "project", WorkspaceID: "workspace", Kind: "memo", Terms: []string{"content"}, Limit: 10})
	if err != nil || keys.Matched != 0 {
		t.Fatalf("JSON field name was treated as searchable text: page=%+v err=%v", keys, err)
	}
}

func TestRecordDatabaseIsIndependentFromCacheDatabase(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	cachePath := filepath.Join(directory, "repoplane.db")
	recordPath := filepath.Join(directory, "records.db")
	cache, err := Open(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	records, err := OpenRecords(ctx, recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateMemo(ctx, store.RecordCreate{Record: testRecord("memo", "memo_durable")}); err != nil {
		t.Fatal(err)
	}
	if err := records.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cachePath); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenRecords(ctx, recordPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.GetRecord(ctx, "project", "workspace", "memo_durable"); err != nil {
		t.Fatalf("durable record did not survive cache deletion: %v", err)
	}
}

func TestOpenRecordsRejectsNewerOrCorruptSchema(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	newer := filepath.Join(directory, "newer.db")
	db, err := sql.Open("sqlite", newer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations VALUES (?, 0)`, recordsSchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenRecords(ctx, newer); err == nil {
		_ = opened.Close()
		t.Fatal("newer durable schema was accepted")
	}
	corrupt := filepath.Join(directory, "corrupt.db")
	if err := os.WriteFile(corrupt, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenRecords(ctx, corrupt); err == nil {
		_ = opened.Close()
		t.Fatal("corrupt durable database was treated as empty")
	}
}
