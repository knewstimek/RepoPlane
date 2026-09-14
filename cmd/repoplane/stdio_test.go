package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"repoplane/internal/application"
	"repoplane/internal/config"
)

func TestStdioHelperProcess(t *testing.T) {
	if os.Getenv("REPOPLANE_STDIO_HELPER") != "1" {
		return
	}
	app, err := application.Open(context.Background(), config.Settings{
		Workspace:      os.Getenv("REPOPLANE_STDIO_WORKSPACE"),
		StateDir:       os.Getenv("REPOPLANE_STDIO_STATE"),
		CatalogRoots:   []string{"catalog"},
		CandidateRoots: []string{},
		RuleFiles:      []string{"AGENTS.md"},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStdioNegotiationHasNoOutputPollution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	workspace := t.TempDir()
	state := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "catalog"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "catalog", "tool.yaml"), []byte(
		"id: test.tool\nrevision: 1\nsummary: stdio test tool\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStdioHelperProcess$")
	command.Env = append(os.Environ(),
		"REPOPLANE_STDIO_HELPER=1",
		"REPOPLANE_STDIO_WORKSPACE="+workspace,
		"REPOPLANE_STDIO_STATE="+state,
	)
	var stderr bytes.Buffer
	command.Stderr = &stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatalf("connect over stdio: %v; stderr=%q", err, stderr.String())
	}

	found := make(map[string]bool)
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		found[tool.Name] = true
	}
	for _, name := range []string{"catalog_query", "workspace_search", "path_explain", "data_query"} {
		if !found[name] {
			t.Fatalf("stdio server did not expose %s; tools=%v", name, found)
		}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "catalog_query", Arguments: map[string]any{"mode": "search", "query": "stdio"},
	})
	if err != nil || result.IsError || result.StructuredContent == nil {
		t.Fatalf("stdio catalog_query failed: result=%+v error=%v", result, err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected server diagnostics: %q", stderr.String())
	}
}
