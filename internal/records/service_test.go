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

func TestTopicMemoReusesCurrentIdentityAndRequiresSupersedeToRename(t *testing.T) {
	service, _ := testService(t)
	request := MemoRequest{Mode: "create", MemoKind: "decision", Scope: "deploy", Configuration: "production", TopicKey: "origin-policy", Content: "Use the configured origin."}
	created, err := service.WriteMemo(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Record.SchemaVersion != "memo.v3" || created.Record.Payload["topic_key"] != "origin-policy" {
		t.Fatalf("topic memo=%+v", created)
	}

	request.TopicKey = "ORIGIN-POLICY"
	existing, err := service.WriteMemo(context.Background(), request)
	if err != nil || existing.Status != contracts.StatusPartial || existing.Record.ID != created.Record.ID || existing.Duplicate || len(existing.Warnings) != 1 || existing.Warnings[0].Code != "memo_topic_exists" {
		t.Fatalf("existing topic response=%+v err=%v", existing, err)
	}

	updated, err := service.WriteMemo(context.Background(), MemoRequest{
		Mode: "update", ID: created.Record.ID, ExpectedRevision: created.Record.Revision,
		MemoKind: "decision", Scope: "deploy", Configuration: "production", TopicKey: "origin-policy", Content: "Use the validated configured origin.",
	})
	if err != nil || updated.Record.Revision != 2 {
		t.Fatalf("topic update=%+v err=%v", updated, err)
	}
	if _, err := service.WriteMemo(context.Background(), MemoRequest{
		Mode: "update", ID: created.Record.ID, ExpectedRevision: updated.Record.Revision,
		MemoKind: "decision", Scope: "deploy", Configuration: "production", TopicKey: "renamed", Content: "renamed",
	}); err == nil || !errors.Is(err, ErrInvalidTransition) || !strings.Contains(err.Error(), "supersede") {
		t.Fatalf("topic rename error=%v", err)
	}

	superseded, err := service.WriteMemo(context.Background(), MemoRequest{Mode: "supersede", ID: created.Record.ID, ExpectedRevision: updated.Record.Revision})
	if err != nil || superseded.Record.Validity != "superseded" {
		t.Fatalf("topic supersede=%+v err=%v", superseded, err)
	}
	replacement, err := service.WriteMemo(context.Background(), request)
	if err != nil || replacement.Record.ID == created.Record.ID || replacement.Status != contracts.StatusOK {
		t.Fatalf("topic replacement=%+v err=%v", replacement, err)
	}
}

func TestTopicMemoSurfacesAtMostThreeRelatedCurrentTopics(t *testing.T) {
	service, _ := testService(t)
	for _, topic := range []string{"alpha", "beta", "gamma", "delta"} {
		response, err := service.WriteMemo(context.Background(), MemoRequest{Mode: "create", MemoKind: "decision", Scope: "runtime", TopicKey: topic, Content: topic})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Warnings) > 3 {
			t.Fatalf("topic %q warnings=%d", topic, len(response.Warnings))
		}
	}
	response, err := service.WriteMemo(context.Background(), MemoRequest{Mode: "create", MemoKind: "decision", Scope: "runtime", TopicKey: "epsilon", Content: "epsilon"})
	if err != nil || len(response.Warnings) != 3 {
		t.Fatalf("related topics=%+v err=%v", response.Warnings, err)
	}
	for _, warning := range response.Warnings {
		if warning.Code != "memo_topic_related" || warning.Ref == nil {
			t.Fatalf("warning=%+v", warning)
		}
	}
}

func TestHostFactIsTypedSearchableAndWarnsOnConflict(t *testing.T) {
	service, _ := testService(t)
	confirmed := time.Date(2026, 9, 16, 1, 2, 3, 0, time.FixedZone("fixture", 9*60*60)).Format(time.RFC3339)
	request := MemoRequest{
		Mode: "create", MemoKind: "host_fact", Scope: "operations/hosts", Source: "user_asserted",
		Host:                  &HostFact{Alias: "EDGE-A", Role: "gateway", OS: "linux", Tier: "production", Services: []string{"proxy"}, Paths: []string{"/srv/proxy"}, ConfirmedAt: confirmed},
		InvalidationCondition: "the host is rebuilt or its role changes",
	}
	created, err := service.WriteMemo(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Record.SchemaVersion != "memo.v2" || created.Record.Payload["content"] != "" || len(created.Warnings) != 0 {
		t.Fatalf("created host fact=%+v", created)
	}
	host := created.Record.Payload["host"].(map[string]any)
	if host["alias"] != "edge-a" || host["confirmed_at"] != "2026-09-15T16:02:03Z" {
		t.Fatalf("normalized host=%+v", host)
	}
	result, err := service.Query(context.Background(), QueryRequest{Mode: "search", Kind: "memo", Query: "edge-a", ItemLimit: 5})
	if err != nil || len(result.Items) != 1 || result.Items[0].Payload["host"] == nil {
		t.Fatalf("host search=%+v err=%v", result, err)
	}
	receiptRequest := request
	receiptRequest.Host = &HostFact{Alias: "edge-b", Role: "gateway", OS: "linux", Tier: "staging", Services: []string{"proxy"}, Paths: []string{"/srv/proxy"}, ConfirmedAt: confirmed}
	receiptRequest.ResponseView = "receipt"
	receipt, err := service.WriteMemo(context.Background(), receiptRequest)
	if err != nil || receipt.Record.SchemaVersion != "memo.v2" || receipt.Record.Payload != nil || receipt.Record.PayloadComplete {
		t.Fatalf("host receipt=%+v err=%v", receipt, err)
	}
	readBack, err := service.Query(context.Background(), QueryRequest{Mode: "get", ID: receipt.Record.ID})
	if err != nil || len(readBack.Items) != 1 || readBack.Items[0].SchemaVersion != "memo.v2" || readBack.Items[0].Payload["host"] == nil {
		t.Fatalf("host receipt read-back=%+v err=%v", readBack, err)
	}
	request.Host = &HostFact{Alias: "edge-a", Role: "database", OS: "windows", Tier: "production", Services: []string{"database"}, Paths: []string{"D:/service"}, ConfirmedAt: time.Now().UTC().Format(time.RFC3339)}
	conflicting, err := service.WriteMemo(context.Background(), request)
	if err != nil || len(conflicting.Warnings) != 1 || conflicting.Warnings[0].Code != "host_fact_conflict" {
		t.Fatalf("conflicting host fact=%+v err=%v", conflicting, err)
	}
}

func TestHostFactValidationIsSeparateFromOrdinaryMemo(t *testing.T) {
	service, _ := testService(t)
	host := &HostFact{Alias: "host-a", Role: "worker", OS: "linux", Tier: "production", Services: []string{"worker"}, Paths: []string{"/srv/worker"}, ConfirmedAt: time.Now().UTC().Format(time.RFC3339)}
	if _, err := service.WriteMemo(context.Background(), MemoRequest{Mode: "create", MemoKind: "decision", Content: "ordinary", Host: host}); err == nil {
		t.Fatal("ordinary memo accepted typed host data")
	}
	if _, err := service.WriteMemo(context.Background(), MemoRequest{Mode: "create", MemoKind: "host_fact", Host: host}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("host fact without invalidation condition error=%v", err)
	}
	host.Services = nil
	if _, err := service.WriteMemo(context.Background(), MemoRequest{
		Mode: "create", MemoKind: "host_fact", Host: host, InvalidationCondition: "the host changes",
	}); !errors.Is(err, ErrInvalidArgument) || !strings.Contains(err.Error(), "bounded services and paths") {
		t.Fatalf("host fact without services error=%v", err)
	}
	host.Services = []string{"worker"}
	ordinary, err := service.WriteMemo(context.Background(), MemoRequest{Mode: "create", MemoKind: "decision", Content: "ordinary"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.WriteMemo(context.Background(), MemoRequest{
		Mode: "update", ID: ordinary.Record.ID, ExpectedRevision: ordinary.Record.Revision,
		MemoKind: "host_fact", Host: host, InvalidationCondition: "the host changes",
	}); err == nil || !errors.Is(err, ErrInvalidTransition) || !strings.Contains(err.Error(), "cannot change memo schema") {
		t.Fatalf("ordinary memo converted to host fact: %v", err)
	}
}

func TestMutationStoreErrorClassifiesStorageFailures(t *testing.T) {
	storageFailure := mutationStoreError(errors.New("database unavailable"))
	if !errors.Is(storageFailure, ErrStorageFailure) {
		t.Fatalf("storage error=%v", storageFailure)
	}
	for _, preserved := range []error{store.ErrConflict, store.ErrNotFound, context.Canceled, context.DeadlineExceeded} {
		if got := mutationStoreError(preserved); !errors.Is(got, preserved) {
			t.Errorf("mutationStoreError(%v)=%v", preserved, got)
		}
	}
}

func TestRecordPayloadProjectionAndMutationReceipt(t *testing.T) {
	service, _ := testService(t)
	created, err := service.WriteMemo(context.Background(), MemoRequest{
		Mode: "create", MemoKind: "decision", Scope: "docs", Content: strings.Repeat("detail ", 200), ResponseView: "receipt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Record.Payload != nil || created.Record.PayloadComplete {
		t.Fatalf("receipt echoed payload: %+v", created.Record)
	}
	encoded, _ := json.Marshal(created)
	if strings.Contains(string(encoded), `"payload"`) {
		t.Fatalf("receipt JSON contains payload: %s", encoded)
	}
	fullMutation, err := service.WriteMemo(context.Background(), MemoRequest{
		Mode: "create", MemoKind: "decision", Scope: "docs", Content: strings.Repeat("detail ", 200),
	})
	if err != nil {
		t.Fatal(err)
	}
	fullMutationJSON, _ := json.Marshal(fullMutation)
	if len(encoded)*2 >= len(fullMutationJSON) {
		t.Fatalf("receipt did not materially reduce echoed content: receipt=%d full=%d", len(encoded), len(fullMutationJSON))
	}

	projected, err := service.Query(context.Background(), QueryRequest{
		Mode: "get", ID: created.Record.ID, PayloadFields: []string{"memo_kind", "scope"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item := projected.Items[0]
	if item.PayloadComplete || len(item.Payload) != 2 || item.Payload["memo_kind"] != "decision" || item.Payload["scope"] != "docs" {
		t.Fatalf("projected record=%+v", item)
	}
	if _, exists := item.Payload["content"]; exists {
		t.Fatal("projection returned an unrequested payload field")
	}

	full, err := service.Query(context.Background(), QueryRequest{Mode: "get", ID: created.Record.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !full.Items[0].PayloadComplete || full.Items[0].Payload["content"] == "" {
		t.Fatalf("full record=%+v", full.Items[0])
	}
	projectedJSON, _ := json.Marshal(projected)
	fullJSON, _ := json.Marshal(full)
	if len(projectedJSON)*2 >= len(fullJSON) {
		t.Fatalf("projection did not materially reduce record content: projected=%d full=%d", len(projectedJSON), len(fullJSON))
	}
}

func TestRecordProjectionSurvivesCursorPagination(t *testing.T) {
	service, _ := testService(t)
	for _, content := range []string{"one", "two"} {
		if _, err := service.WriteMemo(context.Background(), MemoRequest{Mode: "create", MemoKind: "limitation", Scope: "resume", Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := service.Query(context.Background(), QueryRequest{Mode: "list", ItemLimit: 1, PayloadFields: []string{"memo_kind", "scope"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == nil || first.Items[0].PayloadComplete || len(first.Items[0].Payload) != 2 {
		t.Fatalf("first projection=%+v", first)
	}
	second, err := service.Query(context.Background(), QueryRequest{Cursor: *first.NextCursor, ItemLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.Items[0].PayloadComplete || len(second.Items[0].Payload) != 2 {
		t.Fatalf("cursor projection=%+v", second)
	}
}

func TestCompactRecordOptionsRejectInvalidValues(t *testing.T) {
	service, _ := testService(t)
	if _, err := service.WriteMemo(context.Background(), MemoRequest{Mode: "create", MemoKind: "decision", Content: "x", ResponseView: "tiny"}); err == nil {
		t.Fatal("invalid response_view was accepted")
	}
	if _, err := service.Query(context.Background(), QueryRequest{Mode: "list", PayloadFields: []string{"content", "content"}}); err == nil {
		t.Fatal("duplicate payload_fields were accepted")
	}
}

func TestRecordSearchReturnsCompactDiscoveryAndSupportsExplicitProjection(t *testing.T) {
	service, _ := testService(t)
	content := "Windows executable replacement " + strings.Repeat("detail ", 200)
	created, err := service.WriteMemo(context.Background(), MemoRequest{Mode: "create", MemoKind: "resolved_failure", Scope: "release/windows", Configuration: "amd64", Content: content, ResponseView: "receipt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.WriteMemo(context.Background(), MemoRequest{Mode: "create", MemoKind: "decision", Scope: "docs", Content: "unrelated network policy", ResponseView: "receipt"}); err != nil {
		t.Fatal(err)
	}

	result, err := service.Query(context.Background(), QueryRequest{Mode: "search", Kind: "memo", Query: "windows executable", Validity: "current", ItemLimit: 5, ByteLimit: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != created.Record.ID || result.Items[0].PayloadComplete {
		t.Fatalf("compact search=%+v", result)
	}
	payload := result.Items[0].Payload
	if payload["scope"] != "release/windows" || payload["preview"] == "" {
		t.Fatalf("discovery payload=%+v", payload)
	}
	if _, exists := payload["content"]; exists {
		t.Fatal("default search returned full memo content")
	}
	encoded, _ := json.Marshal(result)
	if len(encoded) >= len(content) {
		t.Fatalf("compact search bytes=%d content bytes=%d", len(encoded), len(content))
	}

	projected, err := service.Query(context.Background(), QueryRequest{Mode: "search", Kind: "memo", Query: "windows", PayloadFields: []string{"content"}, ItemLimit: 5})
	if err != nil || len(projected.Items) != 1 || projected.Items[0].Payload["content"] != content {
		t.Fatalf("explicit search projection=%+v err=%v", projected, err)
	}
}

func TestRecordSearchRequiresBoundedQuery(t *testing.T) {
	service, _ := testService(t)
	if _, err := service.Query(context.Background(), QueryRequest{Mode: "search", Kind: "memo"}); err == nil {
		t.Fatal("search without query was accepted")
	}
	if _, err := service.Query(context.Background(), QueryRequest{Mode: "list", Query: "memo"}); err == nil {
		t.Fatal("list with query was accepted")
	}
	if _, err := service.Query(context.Background(), QueryRequest{Mode: "search", Query: strings.Repeat("x", 513)}); !errors.Is(err, contracts.ErrLimitExceeded) {
		t.Fatalf("oversized query error=%v", err)
	}
	if _, err := service.Query(context.Background(), QueryRequest{Mode: "search", Query: strings.Repeat("x", 129)}); !errors.Is(err, contracts.ErrLimitExceeded) {
		t.Fatalf("oversized term error=%v", err)
	}
}

func TestDiscoveryValuesAreDeterministicallyBounded(t *testing.T) {
	preview := compactPreview("  하나\n둘  "+strings.Repeat("셋", 400), 320)
	if len([]rune(preview)) != 320 || !strings.HasPrefix(preview, "하나 둘 셋") || !strings.HasSuffix(preview, "…") {
		t.Fatalf("preview=%q runes=%d", preview, len([]rune(preview)))
	}
	values := make([]any, 12)
	for index := range values {
		values[index] = strings.Repeat("x", 400)
	}
	compact, ok := compactDiscoveryValue(values).([]any)
	if !ok || len(compact) != 8 || len([]rune(compact[0].(string))) != 320 {
		t.Fatalf("compact values=%+v", compact)
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
	refs, qualified, err := service.ResolveCacheQualifications(context.Background(), "dev.verify", "default", []string{"test.verify"})
	if err != nil || !qualified || len(refs) != 1 || refs[0] != "record:"+first.Record.ID {
		t.Fatalf("qualification refs=%v qualified=%v err=%v", refs, qualified, err)
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
	if refs, qualified, err := service.ResolveCacheQualifications(context.Background(), "dev.verify", "default", []string{"test.verify"}); err != nil || qualified || len(refs) != 0 {
		t.Fatalf("stale qualification refs=%v qualified=%v err=%v", refs, qualified, err)
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
