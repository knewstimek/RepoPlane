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
	want := filepath.Join(root.Resolved(), "inside.txt")
	if got != want {
		t.Fatalf("resolved path = %q, want %q", got, want)
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

func TestRuntimeReadGrantAllowsOnlyApprovedExternalPath(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "root")
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "allowed.txt"), []byte("allowed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "denied.txt"), []byte("denied"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	relativeOutside := filepath.Join("..", "outside")
	grant, err := root.GrantRead(relativeOutside)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ID == "" || grant.Path != filepath.ToSlash(relativeOutside) {
		t.Fatalf("grant=%+v", grant)
	}
	if _, err := root.ResolveExisting(filepath.Join(relativeOutside, "allowed.txt")); err != nil {
		t.Fatalf("approved read failed: %v", err)
	}
	if _, err := root.ResolvePrimaryExisting(filepath.Join(relativeOutside, "allowed.txt")); !errors.Is(err, ErrEscape) {
		t.Fatalf("primary boundary expanded: %v", err)
	}
	if _, err := root.ResolveExisting(filepath.Join("..", "denied.txt")); !errors.Is(err, ErrEscape) {
		t.Fatalf("unapproved sibling escaped: %v", err)
	}
	if !root.RevokeRead(grant.ID) {
		t.Fatal("grant was not revoked")
	}
	if _, err := root.ResolveExisting(filepath.Join(relativeOutside, "allowed.txt")); !errors.Is(err, ErrEscape) {
		t.Fatalf("revoked read remained available: %v", err)
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

func TestPreparedReadGrantDoesNotFollowRetargetedSymlink(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "root")
	first := filepath.Join(parent, "first")
	second := filepath.Join(parent, "second")
	for _, directory := range []string{rootPath, first, second} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(rootPath, "external")
	if err := os.Symlink(first, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	root, err := Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := root.PrepareReadGrant("external")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	root.ApplyReadGrant(plan)
	if _, err := root.ResolveExisting("external"); !errors.Is(err, ErrEscape) {
		t.Fatalf("retargeted symlink inherited approval: %v", err)
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
	if err != nil || exists || resolved != filepath.Join(root.Resolved(), "missing", "leaf.txt") {
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
