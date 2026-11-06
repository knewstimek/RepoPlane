package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"repoplane/internal/config"
	"repoplane/internal/mcpserver"
	"repoplane/internal/records"
	"repoplane/internal/runtimeconfig"
)

func TestReplaceDoesNotClearOmittedValues(t *testing.T) {
	ctx := context.Background()
	app, err := Open(ctx, config.Settings{Workspace: t.TempDir(), StateDir: t.TempDir(), RuleFiles: []string{"AGENTS.md"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if _, err := app.Apply(ctx, runtimeconfig.Request{Action: "replace", Target: "rule_file"}); err == nil {
		t.Fatal("omitted values accepted")
	}
	status, err := app.Status(ctx)
	if err != nil || len(status.Configuration["rule_file"]) != 1 {
		t.Fatalf("omission changed settings: %+v %v", status, err)
	}
	changed, err := app.Apply(ctx, runtimeconfig.Request{Action: "replace", Target: "rule_file", Values: []string{}})
	if err != nil || !changed.Changed || len(changed.Configuration["rule_file"]) != 0 {
		t.Fatalf("explicit clear rejected: %+v %v", changed, err)
	}
}

func TestPartialMemoUpdateOverBothMCPSurfaces(t *testing.T) {
	for _, surface := range []string{mcpserver.SurfaceTypedV1, mcpserver.SurfaceToolboxV1} {
		t.Run(surface, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			app, err := Open(ctx, config.Settings{Workspace: t.TempDir(), StateDir: t.TempDir(), ToolSurface: surface, EnableIntentionWrites: true}, "test")
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			created, err := app.router.WriteMemo(ctx, records.MemoRequest{Mode: "create", MemoKind: "decision", Scope: "build", TopicKey: "patch-policy", Content: "before", Title: "Keep title", Source: "llm_proposed", EvidenceRefs: []string{"record:evidence"}})
			if err != nil {
				t.Fatal(err)
			}
			st, ct := mcp.NewInMemoryTransports()
			server, err := mcpserver.New("test", app.MCPOptions()).Connect(ctx, st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
			session, err := client.Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			call := func(args map[string]any) *mcp.CallToolResult {
				t.Helper()
				params := &mcp.CallToolParams{Name: "memo_write", Arguments: args}
				if surface == mcpserver.SurfaceToolboxV1 {
					described, err := session.CallTool(ctx, &mcp.CallToolParams{Name: mcpserver.ToolboxWrite, Arguments: map[string]any{"action": "describe", "operation": "memo_write"}})
					if err != nil || described.IsError {
						t.Fatalf("describe=%+v %v", described, err)
					}
					data, _ := json.Marshal(described.StructuredContent)
					var contract struct {
						SchemaHandle string `json:"schema_handle"`
					}
					if err := json.Unmarshal(data, &contract); err != nil {
						t.Fatal(err)
					}
					params = &mcp.CallToolParams{Name: mcpserver.ToolboxWrite, Arguments: map[string]any{"action": "call", "operation": "memo_write", "schema_handle": contract.SchemaHandle, "arguments": args}}
				}
				result, err := session.CallTool(ctx, params)
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			result := call(map[string]any{"mode": "update", "id": created.Record.ID, "expected_revision": 1, "content": "after"})
			if result.IsError {
				t.Fatalf("partial update failed: %+v", result)
			}
			read, err := app.router.RecordsQuery(ctx, records.QueryRequest{Mode: "get", ID: created.Record.ID})
			if err != nil || read.Items[0].Payload["title"] != "Keep title" || read.Items[0].Payload["scope"] != "build" || len(read.Items[0].EvidenceRefs) != 1 {
				t.Fatalf("omitted fields lost: %+v %v", read, err)
			}
			result = call(map[string]any{"mode": "update", "id": created.Record.ID, "expected_revision": 2, "title": "", "evidence_refs": []string{}})
			if result.IsError {
				t.Fatalf("clear failed: %+v", result)
			}
			read, err = app.router.RecordsQuery(ctx, records.QueryRequest{Mode: "get", ID: created.Record.ID})
			if err != nil || read.Items[0].Payload["title"] != nil || read.Items[0].Payload["content"] != "after" || len(read.Items[0].EvidenceRefs) != 0 {
				t.Fatalf("wire presence lost: %+v %v", read, err)
			}
			result = call(map[string]any{"mode": "update", "id": created.Record.ID, "expected_revision": 3, "unexpected": true})
			if !result.IsError {
				t.Fatal("custom request decoder accepted an unknown argument")
			}
		})
	}
}
