package runtimeaccess

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"repoplane/internal/workspace"
)

func TestGrantRequiresServerIssuedApprovalAndIsRevocable(t *testing.T) {
	parent := t.TempDir()
	primary := filepath.Join(parent, "workspace")
	if err := os.Mkdir(primary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "context.txt"), []byte("context"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := workspace.Open(primary)
	if err != nil {
		t.Fatal(err)
	}
	service := New(root, true, Initial{})
	path := filepath.ToSlash(filepath.Join("..", "context.txt"))
	token, message, err := service.Begin(KindReadPath, path)
	if err != nil || token == "" || message == "" {
		t.Fatalf("token=%q message=%q err=%v", token, message, err)
	}
	if _, err := root.ResolveExisting(filepath.FromSlash(path)); !errors.Is(err, workspace.ErrEscape) {
		t.Fatalf("proposal changed authority: %v", err)
	}
	response, err := service.Complete(token, true)
	if err != nil || response.Status != "granted" || len(response.Grants) != 1 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if _, err := root.ResolveExisting(filepath.FromSlash(path)); err != nil {
		t.Fatal(err)
	}
	response, err = service.Revoke(response.Grants[0].ID)
	if err != nil || response.Status != "revoked" || len(response.Grants) != 0 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if _, err := root.ResolveExisting(filepath.FromSlash(path)); !errors.Is(err, workspace.ErrEscape) {
		t.Fatalf("revoked grant remained active: %v", err)
	}
}

func TestDeclineAndExpiryFailClosed(t *testing.T) {
	root, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := New(root, true, Initial{})
	token, _, err := service.Begin(KindRunner, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Complete(token, false); !errors.Is(err, ErrDeclined) {
		t.Fatalf("decline error=%v", err)
	}
	token, _, err = service.Begin(KindRunner, "")
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Now().Add(pendingTTL + time.Minute) }
	if _, err := service.Complete(token, true); !errors.Is(err, ErrPending) {
		t.Fatalf("expiry error=%v", err)
	}
	if service.Enabled(KindRunner) {
		t.Fatal("expired request enabled Runner")
	}
}

func TestHostGrantCannotBeRevokedAtRuntime(t *testing.T) {
	root, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := New(root, true, Initial{IntentWrite: true})
	if !service.Enabled(KindIntentWrite) {
		t.Fatal("host grant not enabled")
	}
	if _, err := service.Revoke("host_" + KindIntentWrite); !errors.Is(err, ErrInvalid) {
		t.Fatalf("revoke error=%v", err)
	}
}

func TestRuntimeAccessDisabledForHTTPStyleComposition(t *testing.T) {
	root, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := New(root, false, Initial{})
	if _, _, err := service.Begin(KindRunner, ""); !errors.Is(err, ErrDisabled) {
		t.Fatalf("begin error=%v", err)
	}
}

func TestMemoryExportGrantIsBoundToApprovedDirectory(t *testing.T) {
	root, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	approved, other := t.TempDir(), t.TempDir()
	service := New(root, true, Initial{})
	token, _, err := service.Begin(KindMemoryExport, approved)
	if err != nil {
		t.Fatal(err)
	}
	response, err := service.Complete(token, true)
	if err != nil || len(response.Grants) != 1 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if !service.GrantedPath(KindMemoryExport, approved) || service.GrantedPath(KindMemoryExport, other) {
		t.Fatal("memory export grant was not exact")
	}
	if _, err := service.Revoke(response.Grants[0].ID); err != nil {
		t.Fatal(err)
	}
	if service.GrantedPath(KindMemoryExport, approved) {
		t.Fatal("revoked memory export grant remained active")
	}
}
