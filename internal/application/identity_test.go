package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"repoplane/internal/config"
	"repoplane/internal/records"
	"repoplane/internal/workspace"
)

func TestFailedStartupDoesNotPinWrongLegacyNamespace(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	settings := config.Settings{Workspace: t.TempDir(), StateDir: base}
	// Reproduce a state initialization failure before any project tools are usable.
	if err := os.Mkdir(filepath.Join(base, "records.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, settings, "test")
	if err == nil {
		_ = app.Close()
		t.Fatal("expected state initialization failure")
	}
	marker := filepath.Join(settings.Workspace, ".repoplane", "workspace-id")
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed startup pinned a namespace before legacy adoption: %v", err)
	}
	if err := os.Remove(filepath.Join(base, "records.db")); err != nil {
		t.Fatal(err)
	}
	settings.WorkspaceOrigin = filepath.Join(base, "former-workspace")
	app, err = Open(ctx, settings, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
}

func TestLegacyRecordsSurvivePinMoveAndRestart(t *testing.T) {
	for _, alreadyMoved := range []bool{false, true} {
		t.Run(map[bool]string{false: "pin-before-move", true: "adopt-after-move"}[alreadyMoved], func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			old := filepath.Join(base, "before")
			moved := filepath.Join(base, "after")
			if err := os.Mkdir(old, 0o700); err != nil {
				t.Fatal(err)
			}
			settings := config.Settings{Workspace: old, StateDir: filepath.Join(base, "state")}
			app, err := Open(ctx, settings, "test")
			if err != nil {
				t.Fatal(err)
			}
			memo, err := app.router.WriteMemo(ctx, records.MemoRequest{Mode: "create", MemoKind: "decision", Source: "llm_proposed", Content: "retained decision", Scope: "repository", TopicKey: "move-test"})
			if err != nil {
				_ = app.Close()
				t.Fatal(err)
			}
			cp, err := app.router.WriteCheckpoint(ctx, records.CheckpointRequest{Mode: "create", Goal: "continue after moving", NextAction: "resume"})
			if err != nil {
				_ = app.Close()
				t.Fatal(err)
			}
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			// Remove only the new marker to reproduce a pre-upgrade workspace.
			if err := os.Remove(filepath.Join(old, ".repoplane", "workspace-id")); err != nil {
				t.Fatal(err)
			}
			if !alreadyMoved {
				app, err = Open(ctx, settings, "test")
				if err != nil {
					t.Fatal(err)
				}
				if err := app.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Rename(old, moved); err != nil {
				t.Fatal(err)
			}
			settings.Workspace = moved
			if alreadyMoved {
				settings.WorkspaceOrigin = old
			}
			app, err = Open(ctx, settings, "test")
			if err != nil {
				t.Fatal(err)
			}
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			settings.WorkspaceOrigin = ""
			app, err = Open(ctx, settings, "test")
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			for _, id := range []string{memo.Record.ID, cp.Record.ID} {
				result, err := app.router.RecordsQuery(ctx, records.QueryRequest{Mode: "get", ID: id})
				if err != nil || len(result.Items) != 1 || result.Items[0].ID != id {
					t.Fatalf("record not retained: %+v %v", result, err)
				}
			}
			// A fresh checkout without the private marker must stay independent.
			other := t.TempDir()
			root, err := workspace.Open(other)
			if err != nil || root.ID() == app.router.current.root.ID() {
				t.Fatalf("unrelated workspace shares identity: %v", err)
			}
		})
	}
}
