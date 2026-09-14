//go:build windows

package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResolveForLookupRejectsJunctionEscape(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "root")
	outsidePath := filepath.Join(parent, "outside")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outsidePath, 0o700); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(rootPath, "outside-junction")
	command := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, outsidePath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("junction creation unavailable: %v (%s)", err, output)
	}
	t.Cleanup(func() { _ = os.Remove(junction) })
	root, err := Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := root.ResolveForLookup("outside-junction/missing.txt"); !errors.Is(err, ErrEscape) {
		t.Fatalf("error=%v, want ErrEscape", err)
	}
}
