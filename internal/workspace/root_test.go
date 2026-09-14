package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveExisting(t *testing.T) {
	rootPath := t.TempDir()
	file := filepath.Join(rootPath, "inside.txt")
	if err := os.WriteFile(file, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := root.ResolveExisting("inside.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != file {
		t.Fatalf("resolved path = %q, want %q", got, file)
	}
	if root.ID() == "" || root.ID() == root.Resolved() {
		t.Fatalf("workspace ID must be opaque, got %q", root.ID())
	}
}

func TestResolveRejectsLexicalEscape(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "root")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "outside.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = root.ResolveExisting(filepath.Join("..", "outside.txt"))
	if !errors.Is(err, ErrEscape) {
		t.Fatalf("error = %v, want ErrEscape", err)
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "root")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(rootPath, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	root, err := Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = root.ResolveExisting("link.txt")
	if !errors.Is(err, ErrEscape) {
		t.Fatalf("error = %v, want ErrEscape", err)
	}
}

func TestResolveForLookupHandlesMissingLeafAndRejectsEscapingParent(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "root")
	outsidePath := filepath.Join(parent, "outside")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outsidePath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	resolved, exists, err := root.ResolveForLookup("missing/leaf.txt")
	if err != nil || exists || resolved != filepath.Join(rootPath, "missing", "leaf.txt") {
		t.Fatalf("resolved=%q exists=%v error=%v", resolved, exists, err)
	}
	link := filepath.Join(rootPath, "outside-link")
	if err := os.Symlink(outsidePath, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := root.ResolveForLookup("outside-link/missing.txt"); !errors.Is(err, ErrEscape) {
		t.Fatalf("error=%v, want ErrEscape", err)
	}
}
