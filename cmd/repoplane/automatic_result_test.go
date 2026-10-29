package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStdioAutomaticallyDeduplicatesTypedAndToolboxResults(t *testing.T) {
	for _, surface := range []string{"typed.v1", "toolbox.v1"} {
		t.Run(surface, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStdioHelperProcess$")
			command.Env = append(os.Environ(),
				"REPOPLANE_STDIO_HELPER=1",
				"REPOPLANE_STDIO_WORKSPACE="+t.TempDir(),
				"REPOPLANE_STDIO_STATE="+t.TempDir(),
				"REPOPLANE_STDIO_TOOL_SURFACE="+surface,
				"REPOPLANE_STDIO_RESULT_CONTENT=deduplicated",
			)
			var stderr bytes.Buffer
			command.Stderr = &stderr
			client := mcp.NewClient(&mcp.Implementation{Name: "automatic-content-test", Version: "test"}, nil)
			session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
			if err != nil {
				t.Fatalf("connect: %v stderr=%s", err, stderr.String())
			}
			defer session.Close()
			if !strings.Contains(session.InitializeResult().Instructions, "Result content mode: deduplicated") {
				t.Fatal("automatic mode did not reach stdio server")
			}
			count := 0
			for _, err := range session.Tools(ctx, nil) {
				if err != nil {
					t.Fatal(err)
				}
				count++
			}
			wantCount := 14
			name := "catalog_query"
			arguments := map[string]any{"mode": "search", "query": "missing"}
			if surface == "toolbox.v1" {
				wantCount = 5
				name = "repoplane_read"
				describe, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name,
					Arguments: map[string]any{"action": "describe", "operation": "catalog_query"}})
				if err != nil || describe.IsError || len(describe.Content) != 0 {
					t.Fatalf("describe=%+v err=%v", describe, err)
				}
				encoded, _ := json.Marshal(describe.StructuredContent)
				var contract struct {
					SchemaHandle string `json:"schema_handle"`
				}
				if err := json.Unmarshal(encoded, &contract); err != nil || contract.SchemaHandle == "" {
					t.Fatalf("describe handle missing: %s %v", encoded, err)
				}
				arguments = map[string]any{"action": "call", "operation": "catalog_query",
					"schema_handle": contract.SchemaHandle, "arguments": arguments}
			}
			if count != wantCount {
				t.Fatalf("tool count=%d want=%d", count, wantCount)
			}
			// One ordinary tools/call returns the filtered wire response; no CLI/filter call.
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
			if err != nil || result.IsError || len(result.Content) != 0 || result.StructuredContent == nil {
				t.Fatalf("ordinary call did not deduplicate: %+v err=%v", result, err)
			}
			encoded, _ := json.Marshal(result.StructuredContent)
			if !bytes.Contains(encoded, []byte("catalog_no_match")) || !bytes.Contains(encoded, []byte("warnings")) {
				t.Fatalf("warning evidence lost: %s", encoded)
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("unexpected diagnostics: %s", stderr.String())
			}
		})
	}
}
