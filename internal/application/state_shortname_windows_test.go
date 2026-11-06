//go:build windows

package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"

	"repoplane/internal/config"
	"repoplane/internal/runtimeconfig"
)

// CI temporary directories are often reached through 8.3 short names. A state
// directory spelled that way must be rejected before anything is created.
func TestShortNameStateInsideWorkspaceIsRejectedBeforeCreation(t *testing.T) {
	base := filepath.Join(t.TempDir(), "workspace-with-long-name")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	short := shortPath(t, base)
	if strings.EqualFold(short, base) {
		t.Skip("8.3 short names are disabled on this volume")
	}
	state := filepath.Join(short, "private-state")
	_, err := buildBundle(context.Background(), config.Settings{Workspace: base, StateDir: state}, func() bool { return false })
	if !errors.Is(err, runtimeconfig.ErrStateOverlap) {
		t.Fatalf("buildBundle error=%v, want ErrStateOverlap", err)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected state directory was created: %v", err)
	}
}

func shortPath(t *testing.T, path string) string {
	t.Helper()
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	length, err := windows.GetShortPathName(pointer, &buffer[0], uint32(len(buffer)))
	if err != nil {
		t.Fatal(err)
	}
	return syscall.UTF16ToString(buffer[:length])
}
