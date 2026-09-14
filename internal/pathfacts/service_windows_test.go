//go:build windows

package pathfacts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"repoplane/internal/search"
	"repoplane/internal/workspace"
)

func TestExplainReportsJunction(t *testing.T) {
	rootPath := t.TempDir()
	target := filepath.Join(rootPath, "target")
	junction := filepath.Join(rootPath, "junction")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, target)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("junction unavailable: %v (%s)", err, output)
	}
	root, err := workspace.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(root, searchStub{outcome: search.BackendOutcome{Complete: true}}, nil)
	response, err := service.Explain(context.Background(), Request{Path: "junction"})
	if err != nil {
		t.Fatal(err)
	}
	links := response.Items[0].Links
	if links.ReparsePoint == nil || !*links.ReparsePoint || links.Junction == nil || !*links.Junction {
		t.Fatalf("junction facts=%+v", links)
	}
}
