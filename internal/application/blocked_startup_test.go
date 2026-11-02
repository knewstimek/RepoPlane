package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"repoplane/internal/config"
	"repoplane/internal/mcpserver"
	"repoplane/internal/runtimeconfig"
)

func TestBlockedStartupKeepsMCPConnectedAndRecovers(t *testing.T) {
	for _, surface := range []string{mcpserver.SurfaceTypedV1, mcpserver.SurfaceToolboxV1} {
		for _, target := range []string{"workspace", "state_dir"} {
			t.Run(surface+"/"+target, func(t *testing.T) {
				base := t.TempDir()
				state := filepath.Join(base, "private-state")
				project := filepath.Join(base, "project")
				if err := os.Mkdir(project, 0o700); err != nil {
					t.Fatal(err)
				}
				app, err := Open(context.Background(), config.Settings{
					Workspace: base, StateDir: state, ToolSurface: surface,
					ResultContent: "deduplicated",
				}, "test")
				if err != nil {
					t.Fatal(err)
				}
				defer app.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				serverTransport, clientTransport := mcp.NewInMemoryTransports()
				serverSession, err := mcpserver.New("test", app.MCPOptions()).Connect(ctx, serverTransport, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer serverSession.Close()
				client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
				session, err := client.Connect(ctx, clientTransport, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer session.Close()
				list, err := session.ListTools(ctx, nil)
				wantTools := 14
				if surface == mcpserver.SurfaceToolboxV1 {
					wantTools = 5
				}
				if err != nil || len(list.Tools) != wantTools {
					t.Fatalf("tools=%+v err=%v", list, err)
				}
				call := func(name, toolbox string, arguments map[string]any) *mcp.CallToolResult {
					t.Helper()
					params := &mcp.CallToolParams{Name: name, Arguments: arguments}
					if surface == mcpserver.SurfaceToolboxV1 {
						described, err := session.CallTool(ctx, &mcp.CallToolParams{Name: toolbox,
							Arguments: map[string]any{"action": "describe", "operation": name}})
						if err != nil || described.IsError {
							t.Fatalf("describe=%+v err=%v", described, err)
						}
						raw, _ := json.Marshal(described.StructuredContent)
						var contract struct {
							SchemaHandle string `json:"schema_handle"`
						}
						if err := json.Unmarshal(raw, &contract); err != nil {
							t.Fatal(err)
						}
						params = &mcp.CallToolParams{Name: toolbox, Arguments: map[string]any{
							"action": "call", "operation": name, "schema_handle": contract.SchemaHandle, "arguments": arguments}}
					}
					result, err := session.CallTool(ctx, params)
					if err != nil {
						t.Fatal(err)
					}
					return result
				}
				status := call("runtime_config", mcpserver.ToolboxState, map[string]any{"action": "status"})
				raw, _ := json.Marshal(status)
				if status.IsError || !strings.Contains(string(raw), "blocked") || !strings.Contains(string(raw), "workspace_state_overlap") {
					t.Fatalf("status=%s", raw)
				}
				blocked := []struct {
					name, toolbox string
					args          map[string]any
				}{
					{"catalog_query", mcpserver.ToolboxRead, map[string]any{"mode": "status"}},
					{"workspace_search", mcpserver.ToolboxRead, map[string]any{"mode": "exact", "pattern": "example"}},
					{"path_explain", mcpserver.ToolboxRead, map[string]any{"path": "."}},
					{"data_query", mcpserver.ToolboxRead, map[string]any{}},
					{"project_records", mcpserver.ToolboxRead, map[string]any{"mode": "list"}},
					{"checkpoint_write", mcpserver.ToolboxWrite, map[string]any{"mode": "create"}},
					{"memo_write", mcpserver.ToolboxWrite, map[string]any{"mode": "create", "source": "llm_proposed"}},
					{"check_report_import", mcpserver.ToolboxImport, map[string]any{"path": "report.json", "checklist_path": "check.yaml", "configuration": "test"}},
					{"run_prepare", mcpserver.ToolboxRunner, map[string]any{"capability_id": "test", "capability_revision": "1"}},
					{"run_execute", mcpserver.ToolboxRunner, map[string]any{"plan_id": "test"}},
					{"run_inspect", mcpserver.ToolboxRunner, map[string]any{"run_id": "test"}},
					{"memory_backup", mcpserver.ToolboxState, map[string]any{"destination": "export"}},
					{"runtime_access", mcpserver.ToolboxRead, map[string]any{"action": "grant", "kind": "runner"}},
				}
				for _, test := range blocked {
					result := call(test.name, test.toolbox, test.args)
					raw, _ := json.Marshal(result.Content)
					if !result.IsError || !strings.Contains(string(raw), "workspace_state_overlap") ||
						!strings.Contains(string(raw), "runtime_config") || strings.Contains(string(raw), base) {
						t.Fatalf("%s: %s", test.name, raw)
					}
				}
				// Invalid changes leave the connection blocked; no approval or state writes occur.
				result := call("runtime_config", mcpserver.ToolboxState, map[string]any{
					"action": "select", "target": "state_dir", "values": []string{filepath.Join(base, "other-state")}})
				if !result.IsError {
					t.Fatal("overlapping selection succeeded")
				}
				if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("blocked startup created state: %v", err)
				}
				if _, err := os.Stat(filepath.Join(base, "other-state")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed selection created state: %v", err)
				}
				value := project
				if target == "state_dir" {
					value = t.TempDir()
				}
				result = call("runtime_config", mcpserver.ToolboxState, map[string]any{
					"action": "select", "target": target, "values": []string{value}})
				if result.IsError {
					t.Fatalf("recovery=%+v", result)
				}
				current, err := app.Status(ctx)
				if err != nil || current.Status != "ok" || len(current.Warnings) != 0 {
					t.Fatalf("recovered status=%+v err=%v", current, err)
				}
				result = call("catalog_query", mcpserver.ToolboxRead, map[string]any{"mode": "status"})
				if result.IsError {
					t.Fatalf("same-session query after recovery=%+v", result)
				}
				// Returning an active server to an overlapping layout must fail atomically.
				_, err = app.Apply(ctx, runtimeconfig.Request{Action: "select", Target: "state_dir", Values: []string{filepath.Join(current.Configuration["workspace"][0], "invalid-state")}})
				if !errors.Is(err, runtimeconfig.ErrStateOverlap) {
					t.Fatalf("unsafe reconfiguration=%v", err)
				}
				if current, _ := app.Status(ctx); current.Status != "ok" {
					t.Fatalf("failed reconfiguration disabled working server: %+v", current)
				}
			})
		}
	}
}
