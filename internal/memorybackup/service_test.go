package memorybackup

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"repoplane/internal/store"
	storesqlite "repoplane/internal/store/sqlite"
	"repoplane/internal/workspace"
)

type idleGuard struct{}

func (idleGuard) BeginMemorySnapshot() (func(), error) { return func() {}, nil }

type busyGuard struct{}

func (busyGuard) BeginMemorySnapshot() (func(), error) { return nil, errors.New("busy") }

func TestPortableMemoryExportRestoreRebindsAndExcludesSecrets(t *testing.T) {
	base := t.TempDir()
	sourceRoot := filepath.Join(base, "source-workspace")
	sourceState := filepath.Join(base, "source-state")
	destination := filepath.Join(base, "backup-destination")
	for _, directory := range []string{sourceRoot, sourceState, destination} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	root, err := workspace.Open(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := storesqlite.OpenRecords(context.Background(), filepath.Join(sourceState, "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000_000_000, 0).UTC()
	record := store.Record{ID: "checkpoint_backup", Kind: "checkpoint", SchemaVersion: "checkpoint.v1", ProjectID: root.ID(), WorkspaceID: root.ID(), Revision: 1, Source: "user_asserted", WriterClass: "intention", Validity: "current", CreatedAt: now, UpdatedAt: now, Payload: json.RawMessage(`{"goal":"portable"}`), EvidenceRefs: []string{}}
	if _, err := repository.CreateCheckpoint(context.Background(), store.RecordCreate{Record: record}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sourceState, "runs", "run_1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceState, "runs", "run_1", "stdout.log"), []byte("evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceState, "cursor.key"), []byte("must-not-export"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := New(root, repository, idleGuard{}, sourceState)
	response, err := service.Export(context.Background(), Request{Destination: destination})
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(destination, response.ArchiveName)
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range reader.File {
		if entry.Name == "cursor.key" || entry.Name == "records.db" {
			t.Fatalf("secret or machine-bound database exported: %s", entry.Name)
		}
	}
	_ = reader.Close()
	_ = repository.Close()

	targetRoot := filepath.Join(base, "target-workspace")
	targetState := filepath.Join(base, "target-state")
	if err := os.Mkdir(targetRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(targetState, 0o700); err != nil {
		t.Fatal(err)
	}
	reboundRoot, err := workspace.Open(targetRoot)
	if err != nil {
		t.Fatal(err)
	}
	targetRepository, err := storesqlite.OpenRecords(context.Background(), filepath.Join(targetState, "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer targetRepository.Close()
	targetService := New(reboundRoot, targetRepository, idleGuard{}, targetState)
	if _, err := targetService.Restore(context.Background(), archivePath, 0); err != nil {
		t.Fatal(err)
	}
	restored, err := targetRepository.GetRecord(context.Background(), reboundRoot.ID(), reboundRoot.ID(), record.ID)
	if err != nil || string(restored.Payload) != string(record.Payload) {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
	if content, err := os.ReadFile(filepath.Join(targetState, "runs", "run_1", "stdout.log")); err != nil || string(content) != "evidence\n" {
		t.Fatalf("restored stream=%q err=%v", content, err)
	}
}

func TestExportRejectsWorkspaceAndStateDestinations(t *testing.T) {
	base := t.TempDir()
	rootPath := filepath.Join(base, "workspace")
	state := filepath.Join(base, "state")
	_ = os.Mkdir(rootPath, 0o700)
	_ = os.Mkdir(state, 0o700)
	root, _ := workspace.Open(rootPath)
	repository, err := storesqlite.OpenRecords(context.Background(), filepath.Join(state, "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	service := New(root, repository, idleGuard{}, state)
	destinations := []string{rootPath, state}
	if runtime.GOOS == "windows" {
		destinations = append(destinations, strings.ToUpper(state))
	}
	for _, destination := range destinations {
		if _, err := service.Export(context.Background(), Request{Destination: destination}); err == nil {
			t.Fatalf("unsafe destination accepted: %s", destination)
		}
	}
}

func TestExportFailsClosedWhenRunnerIsActive(t *testing.T) {
	base := t.TempDir()
	rootPath, state, destination := filepath.Join(base, "workspace"), filepath.Join(base, "state"), filepath.Join(base, "destination")
	for _, directory := range []string{rootPath, state, destination} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	root, _ := workspace.Open(rootPath)
	repository, err := storesqlite.OpenRecords(context.Background(), filepath.Join(state, "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	service := New(root, repository, busyGuard{}, state)
	if _, err := service.Export(context.Background(), Request{Destination: destination}); !errors.Is(err, ErrNotIdle) {
		t.Fatalf("active Runner error=%v", err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial backup files=%v err=%v", entries, err)
	}
}
