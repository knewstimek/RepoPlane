package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPinnedIdentityMovesAndLegacyOrigin(t *testing.T) {
	base := t.TempDir()
	old := filepath.Join(base, "before")
	newPath := filepath.Join(base, "after")
	if err := os.Mkdir(old, 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := Open(old)
	if err != nil {
		t.Fatal(err)
	}
	legacy := r.ID()
	if err := r.PinIdentity(""); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(old, newPath); err != nil {
		t.Fatal(err)
	}
	moved, err := Open(newPath)
	if err != nil || moved.ID() != legacy {
		t.Fatalf("move lost identity: %v", err)
	}
	if err := moved.PinIdentity(filepath.Join(base, "unrelated")); err == nil {
		t.Fatal("conflicting origin accepted")
	}
	if err := os.Remove(filepath.Join(newPath, identityFile)); err != nil {
		t.Fatal(err)
	}
	legacyMoved, err := Open(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if legacyMoved.ID() == legacy {
		t.Fatal("unpinned paths unexpectedly match")
	}
	if err := legacyMoved.PinIdentity(old); err != nil {
		t.Fatal(err)
	}
	if legacyMoved.ID() != legacy {
		t.Fatal("missing legacy path not adopted")
	}
	if err := os.WriteFile(filepath.Join(newPath, identityFile), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(newPath); err == nil {
		t.Fatal("corrupt identity silently reset")
	}
}
