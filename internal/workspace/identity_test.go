package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentPinPublishesCompleteIdentity(t *testing.T) {
	path := t.TempDir()
	const openers = 8
	roots := make([]*Root, openers)
	for i := range roots {
		var err error
		roots[i], err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, openers)
	var writers sync.WaitGroup
	for _, root := range roots {
		writers.Go(func() {
			<-start
			results <- root.PinIdentity("")
		})
	}
	close(start)
	writers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	saved, err := Open(path)
	if err != nil || saved.ID() != roots[0].ID() {
		t.Fatalf("incomplete or changed identity: %v", err)
	}
	staged, err := filepath.Glob(filepath.Join(path, ".repoplane", ".workspace-id-*"))
	if err != nil || len(staged) != 0 {
		t.Fatalf("staging files remain: %d %v", len(staged), err)
	}
}

func TestCompetingLegacyOriginsCannotReplaceWinner(t *testing.T) {
	path := t.TempDir()
	one, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, root := range []*Root{one, two} {
		if err := root.PrepareIdentity(filepath.Join(path, []string{"former-one", "former-two"}[i])); err != nil {
			t.Fatal(err)
		}
	}
	if err := one.PinIdentity(""); err != nil {
		t.Fatal(err)
	}
	if err := two.PinIdentity(""); err == nil {
		t.Fatal("prepared services rebound to another opener's namespace")
	}
	saved, err := Open(path)
	if err != nil || saved.ID() != one.ID() {
		t.Fatalf("winner overwritten: %v", err)
	}
}

func TestIdentityCannotUseExternalDirectory(t *testing.T) {
	path, external := t.TempDir(), t.TempDir()
	if err := os.Symlink(external, filepath.Join(path, ".repoplane")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("external identity directory accepted")
	}
	entries, err := os.ReadDir(external)
	if err != nil || len(entries) != 0 {
		t.Fatalf("external directory was modified: %v", err)
	}
}

func TestOversizedIdentityIsRejected(t *testing.T) {
	root, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.PinIdentity(""); err != nil {
		t.Fatal(err)
	}
	// A valid prefix must not hide corrupt data beyond the bounded read.
	data := root.ID() + strings.Repeat(" ", 128) + "corrupt suffix"
	if err := os.WriteFile(filepath.Join(root.Resolved(), identityFile), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root.Resolved()); err == nil {
		t.Fatal("oversized identity accepted after truncating its corrupt suffix")
	}
}

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
