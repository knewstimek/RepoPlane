package dataquery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"repoplane/internal/contracts"
	"repoplane/internal/cursor"
	"repoplane/internal/store"
	storesqlite "repoplane/internal/store/sqlite"
	"repoplane/internal/textcodec"
	"repoplane/internal/workspace"
)

func newDataService(t *testing.T) (*Service, string) {
	t.Helper()
	rootPath := t.TempDir()
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
	service := NewService(root, repository, codec)
	service.now = func() time.Time { return now }
	return service, rootPath
}

func TestTextLineAndByteRanges(t *testing.T) {
	service, root := newDataService(t)
	encoded, err := textcodec.Encode("cp949", "첫째\r\n둘째\n셋째\r")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "text.txt"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	lineStart, lineEnd := uint64(2), uint64(3)
	response, err := service.Query(context.Background(), Request{
		Mode: "text_range", Ref: "source:mutable:text.txt", Encoding: "cp949",
		LineStart: &lineStart, LineEnd: &lineEnd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 2 || response.Items[0].Line != 2 || response.Items[0].Text != "둘째" || response.Items[1].Text != "셋째" {
		t.Fatalf("unexpected line range: %+v", response.Items)
	}
	byteStart, byteEnd := uint64(0), uint64(2)
	bytesResponse, err := service.Query(context.Background(), Request{
		Mode: "text_range", Ref: "source:mutable:text.txt", Encoding: "cp949",
		ByteStart: &byteStart, ByteEnd: &byteEnd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bytesResponse.Items) != 1 || bytesResponse.Items[0].BytesBase64 == "" {
		t.Fatalf("unexpected byte range: %+v", bytesResponse.Items)
	}
}

func TestJSONLPreservesLargeIntegerProjectionAndPagination(t *testing.T) {
	service, root := newDataService(t)
	data := strings.Join([]string{
		`{"id":9007199254740993,"status":"failed","extra":"a"}`,
		`{"id":9007199254740993,"status":"failed","extra":"b"}`,
		`{"id":9007199254740993,"status":"failed","extra":"c"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, "records.jsonl"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	response, err := service.Query(context.Background(), Request{
		Mode: "jsonl", Ref: "source:mutable:records.jsonl", Encoding: "utf-8",
		Filters: []Filter{{Field: "id", ValueType: "integer", Value: "9007199254740993"}},
		Fields:  []string{"id", "status", "missing"}, ItemLimit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 2 || response.NextCursor == nil || response.Counts.Matched == nil || *response.Counts.Matched != 3 {
		t.Fatalf("unexpected first JSONL page: %+v", response)
	}
	encodedResponse, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encodedResponse), `9007199254740993`) || strings.Contains(string(encodedResponse), `9007199254740992`) {
		t.Fatalf("large integer lost precision: %s", encodedResponse)
	}
	if value, ok := response.Items[0].Record["missing"]; !ok || value != nil {
		t.Fatalf("missing projection policy not applied: %+v", response.Items[0].Record)
	}
	second, err := service.Query(context.Background(), Request{Cursor: *response.NextCursor, ItemLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.NextCursor != nil {
		t.Fatalf("unexpected second page: %+v", second)
	}
}

func TestJSONLMalformedSkipIsPartialLowerBound(t *testing.T) {
	service, root := newDataService(t)
	data := "{\"id\":1}\nnot-json\n{\"id\":2}\n"
	if err := os.WriteFile(filepath.Join(root, "records.jsonl"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	response, err := service.Query(context.Background(), Request{
		Mode: "jsonl", Ref: "source:mutable:records.jsonl", MalformedPolicy: "skip_with_warning",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "partial" || response.Counts.Relation != "lower_bound" || response.Scan.State != "complete" || len(response.Warnings) != 1 {
		t.Fatalf("unexpected malformed response: %+v", response)
	}
}

func TestJSONLMalformedFailRejectsTrailingData(t *testing.T) {
	service, root := newDataService(t)
	if err := os.WriteFile(filepath.Join(root, "records.jsonl"), []byte("{\"id\":1} trailing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := service.Query(context.Background(), Request{
		Mode: "jsonl", Ref: "source:mutable:records.jsonl", MalformedPolicy: "fail",
	})
	if err == nil || !strings.Contains(err.Error(), "malformed JSONL record") {
		t.Fatalf("error=%v, want malformed record", err)
	}
}

func TestJSONLOversizedRecordHasExplicitPolicy(t *testing.T) {
	service, root := newDataService(t)
	record := "{\"value\":\"" + strings.Repeat("x", int(contracts.MaximumJSONLRecord)) + "\"}\n"
	if err := os.WriteFile(filepath.Join(root, "oversized.jsonl"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := service.Query(context.Background(), Request{
		Mode: "jsonl", Ref: "source:mutable:oversized.jsonl", MalformedPolicy: "fail",
	})
	if err == nil || !strings.Contains(err.Error(), "token too long") {
		t.Fatalf("fail policy error=%v", err)
	}
	response, err := service.Query(context.Background(), Request{
		Mode: "jsonl", Ref: "source:mutable:oversized.jsonl", MalformedPolicy: "skip_with_warning",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "partial" || response.Scan.State != "partial" || len(response.Warnings) != 1 || response.Warnings[0].Code != "oversized_record" {
		t.Fatalf("skip policy response=%+v", response)
	}
}

func TestContentHashRefDetectsSourceChange(t *testing.T) {
	service, root := newDataService(t)
	original := []byte("original\n")
	path := filepath.Join(root, "source.txt")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(original)
	ref := "source:source.txt@sha256:" + hex.EncodeToString(sum[:])
	if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start, end := uint64(1), uint64(1)
	_, err := service.Query(context.Background(), Request{Mode: "text_range", Ref: ref, LineStart: &start, LineEnd: &end})
	if !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("error=%v, want ErrSourceChanged", err)
	}
}

func TestCursorDetectsSourceChangeBetweenPages(t *testing.T) {
	service, root := newDataService(t)
	path := filepath.Join(root, "records.jsonl")
	if err := os.WriteFile(path, []byte("{\"id\":1}\n{\"id\":2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	response, err := service.Query(context.Background(), Request{
		Mode: "jsonl", Ref: "source:mutable:records.jsonl", ItemLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.NextCursor == nil {
		t.Fatal("first page did not return a cursor")
	}
	if err := os.WriteFile(path, []byte("{\"id\":1}\n{\"id\":3}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = service.Query(context.Background(), Request{Cursor: *response.NextCursor})
	if !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("error=%v, want ErrSourceChanged", err)
	}
}
