package application

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"repoplane/internal/catalog"
	"repoplane/internal/config"
	"repoplane/internal/mcpserver"
	"repoplane/internal/runtimeconfig"
	"repoplane/internal/search"
)

func TestApplicationReconfiguresSourcesWorkspaceStateAndHTTPAtRuntime(t *testing.T) {
	base := t.TempDir()
	workspaceOne := filepath.Join(base, "workspace-one")
	workspaceTwo := filepath.Join(base, "workspace-two")
	stateOne := filepath.Join(base, "state-one")
	stateTwo := filepath.Join(base, "state-two")
	for _, directory := range []string{workspaceOne, workspaceTwo, stateOne, stateTwo, filepath.Join(workspaceOne, "catalog"), filepath.Join(workspaceOne, "extra"), filepath.Join(workspaceOne, "tools")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspaceOne, "catalog", "one.yaml"), []byte("id: test.one\nrevision: 1\nsummary: one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceOne, "extra", "two.yaml"), []byte("id: test.two\nrevision: 1\nsummary: two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceOne, "RULES.md"), []byte("runtime rules\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceOne, "symbols.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceTwo, "marker.txt"), []byte("second workspace\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	app, err := Open(context.Background(), config.Settings{Workspace: workspaceOne, StateDir: stateOne, CatalogRoots: []string{"catalog"}, CandidateRoots: []string{}, RuleFiles: []string{"AGENTS.md"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for _, request := range []runtimeconfig.Request{
		{Action: "add", Target: "catalog_root", Values: []string{"extra"}},
		{Action: "add", Target: "candidate_root", Values: []string{"tools"}},
		{Action: "add", Target: "rule_file", Values: []string{"RULES.md"}},
		{Action: "add", Target: "symbol_index", Values: []string{"symbols.jsonl"}},
	} {
		response, err := app.Apply(ctx, request)
		if err != nil || !response.Changed || !response.Refreshed {
			t.Fatalf("request=%+v response=%+v err=%v", request, response, err)
		}
	}
	query, err := app.router.Query(ctx, catalog.QueryRequest{Mode: "search", Query: "test.two"})
	if err != nil || len(query.Items) != 1 {
		t.Fatalf("catalog result=%+v err=%v", query, err)
	}
	status, err := app.Status(ctx)
	if err != nil || len(status.Configuration["catalog_root"]) != 2 || len(status.Configuration["candidate_root"]) != 1 || len(status.Configuration["rule_file"]) != 2 || len(status.Configuration["symbol_index"]) != 1 || len(status.Configuration["tool_surface"]) != 1 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if response, err := app.Apply(ctx, runtimeconfig.Request{Action: "remove", Target: "candidate_root", Values: []string{"tools"}}); err != nil || !response.Changed {
		t.Fatalf("remove response=%+v err=%v", response, err)
	}
	if response, err := app.Apply(ctx, runtimeconfig.Request{Action: "replace", Target: "rule_file", Values: []string{"RULES.md"}}); err != nil || !response.Changed {
		t.Fatalf("replace response=%+v err=%v", response, err)
	}
	if response, err := app.Apply(ctx, runtimeconfig.Request{Action: "refresh", Target: "symbol_index"}); err != nil || response.Changed || !response.Refreshed {
		t.Fatalf("refresh response=%+v err=%v", response, err)
	}
	if response, err := app.Apply(ctx, runtimeconfig.Request{Action: "select", Target: "state_dir", Values: []string{stateTwo}}); err != nil || !response.Changed {
		t.Fatalf("state response=%+v err=%v", response, err)
	}
	if _, err := os.Stat(filepath.Join(stateTwo, "records.db")); err != nil {
		t.Fatal(err)
	}
	if response, err := app.Apply(ctx, runtimeconfig.Request{Action: "select", Target: "workspace", Values: []string{workspaceTwo}}); err != nil || !response.Changed {
		t.Fatalf("workspace response=%+v err=%v", response, err)
	}
	searchResult, err := app.router.SearchQuery(ctx, search.Request{Mode: "exact", Pattern: "second workspace"})
	if err != nil || len(searchResult.Items) != 1 {
		t.Fatalf("search=%+v err=%v", searchResult, err)
	}

	tokenPath := filepath.Join(base, "token")
	profilePath := filepath.Join(base, "http.yaml")
	if err := os.WriteFile(tokenPath, []byte(strings.Repeat("a", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := "listen: 127.0.0.1:0\nendpoint: /mcp\nresource_uri: http://127.0.0.1/mcp\nallowed_hosts: [127.0.0.1]\nallowed_origins: []\nauth:\n  mode: local_token\n  token_file: " + filepath.ToSlash(tokenPath) + "\n  scopes: [repoplane.read]\nlimits:\n  body_bytes: 1048576\n  concurrent_requests: 4\n  per_principal: 2\n  requests_per_minute: 60\naudit:\n  retention_days: 30\n"
	if err := os.WriteFile(profilePath, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	if response, err := app.Apply(ctx, runtimeconfig.Request{Action: "start", Target: "http_transport", Values: []string{profilePath}}); err != nil || len(response.Configuration["http_transport"]) != 1 {
		t.Fatalf("HTTP start=%+v err=%v", response, err)
	}
	if response, err := app.Apply(ctx, runtimeconfig.Request{Action: "stop", Target: "http_transport"}); err != nil || len(response.Configuration["http_transport"]) != 0 {
		t.Fatalf("HTTP stop=%+v err=%v", response, err)
	}
}

func TestRuntimeConfigurationUsesOneShotMCPApproval(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "extra"), 0o700); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), config.Settings{Workspace: workspace, StateDir: t.TempDir(), CatalogRoots: []string{}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := mcpserver.New("test", app.MCPOptions()).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	approvals := 0
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		approvals++
		return &mcp.ElicitResult{Action: "accept"}, nil
	}})
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "runtime_config", Arguments: map[string]any{"action": "add", "target": "catalog_root", "values": []string{"extra"}}})
	if err != nil || result.IsError || result.StructuredContent == nil || approvals != 1 {
		t.Fatalf("result=%+v approvals=%d err=%v", result, approvals, err)
	}
	status, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "runtime_config", Arguments: map[string]any{"action": "status"}})
	if err != nil || status.IsError || approvals != 1 {
		t.Fatalf("status=%+v approvals=%d err=%v", status, approvals, err)
	}
}

func TestApplicationExposesCatalogQuery(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "catalog"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "catalog", "tool.yaml"), []byte(
		"id: test.tool\nrevision: 1\nsummary: test tool\ntags: [test]\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), config.Settings{
		Workspace: workspace, StateDir: t.TempDir(), CatalogRoots: []string{"catalog"},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := mcpserver.New("test", app.MCPOptions()).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	found := map[string]bool{}
	for tool, err := range clientSession.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		found[tool.Name] = true
	}
	if !found["catalog_query"] || !found["workspace_search"] || !found["path_explain"] || !found["data_query"] || !found["project_records"] {
		t.Fatalf("expected tools not exposed: %v", found)
	}
	for _, name := range []string{"runtime_access", "runtime_config", "memory_backup", "checkpoint_write", "memo_write", "check_report_import", "run_prepare", "run_execute", "run_inspect"} {
		if !found[name] {
			t.Fatalf("runtime-approved tool %s not exposed: %v", name, found)
		}
	}
	if len(found) != 14 {
		t.Fatalf("tool count=%d, want 14: %v", len(found), found)
	}
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "catalog_query", Arguments: map[string]any{"mode": "search", "query": "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("catalog_query returned tool error: %+v", result.Content)
	}
	if result.StructuredContent == nil {
		t.Fatal("catalog_query did not return structured content")
	}
	searchResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "workspace_search", Arguments: map[string]any{"mode": "exact", "pattern": "test tool"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if searchResult.IsError || searchResult.StructuredContent == nil {
		t.Fatalf("workspace_search failed: %+v", searchResult)
	}
	pathResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "path_explain", Arguments: map[string]any{"path": "catalog/tool.yaml", "encoding": "utf-8"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pathResult.IsError || pathResult.StructuredContent == nil {
		t.Fatalf("path_explain failed: %+v", pathResult)
	}
	dataResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "data_query", Arguments: map[string]any{
			"mode": "text_range", "ref": "source:mutable:catalog/tool.yaml", "line_start": 1, "line_end": 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dataResult.IsError || dataResult.StructuredContent == nil {
		t.Fatalf("data_query failed: %+v", dataResult)
	}
}

func TestApplicationPreservesHostGrantedRunnerAndCache(t *testing.T) {
	workspace := t.TempDir()
	state := t.TempDir()
	app, err := Open(context.Background(), config.Settings{
		Workspace: workspace, StateDir: state, CatalogRoots: []string{"catalog"}, EnableRunner: true, EnableCache: true,
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	found := map[string]bool{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		found[tool.Name] = true
	}
	for _, name := range []string{"run_prepare", "run_execute", "run_inspect"} {
		if !found[name] {
			t.Fatalf("%s not exposed: %v", name, found)
		}
	}
	if len(found) != 14 {
		t.Fatalf("stable runtime tool set changed: %v", found)
	}
	if info, err := os.Stat(filepath.Join(state, "cache.key")); err != nil || info.Size() != 32 {
		t.Fatalf("cache key info=%v err=%v", info, err)
	}
}

func TestApplicationGrantsExternalReadAndWriteAtRuntime(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "context.txt"), []byte("runtime context\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), config.Settings{
		Workspace: workspace, StateDir: t.TempDir(), CatalogRoots: []string{"catalog"},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := mcpserver.New("test", app.MCPOptions()).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	approvals := 0
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, &mcp.ClientOptions{
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			approvals++
			return &mcp.ElicitResult{Action: "accept"}, nil
		},
	})
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	read, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "data_query", Arguments: map[string]any{
		"mode": "text_range", "ref": "source:mutable:../context.txt", "line_start": 1, "line_end": 1,
	}})
	if err != nil || read.IsError || read.StructuredContent == nil {
		t.Fatalf("runtime read result=%+v error=%v", read, err)
	}
	write, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "checkpoint_write", Arguments: map[string]any{"mode": "create", "goal": "runtime approval"}})
	if err != nil || write.IsError || write.StructuredContent == nil {
		t.Fatalf("runtime write result=%+v error=%v", write, err)
	}
	if approvals != 2 {
		t.Fatalf("approval prompts=%d, want 2", approvals)
	}
}

func TestApplicationExportsPortableMemoryAtRuntime(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	state := filepath.Join(base, "state")
	destination := filepath.Join(base, "backup")
	for _, directory := range []string{workspace, state, destination} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	app, err := Open(context.Background(), config.Settings{Workspace: workspace, StateDir: state, CatalogRoots: []string{"catalog"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := mcpserver.New("test", app.MCPOptions()).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	approvals := 0
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		approvals++
		return &mcp.ElicitResult{Action: "accept"}, nil
	}})
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "checkpoint_write", Arguments: map[string]any{"mode": "create", "goal": "portable runtime memory"}}); err != nil || result.IsError {
		t.Fatalf("checkpoint result=%+v err=%v", result, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "memory_backup", Arguments: map[string]any{"destination": destination}})
	if err != nil || result.IsError || result.StructuredContent == nil {
		t.Fatalf("memory backup result=%+v err=%v", result, err)
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	var receipt struct {
		ArchiveName string `json:"archive_name"`
		Records     uint64 `json:"records"`
		Portable    bool   `json:"portable"`
	}
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.ArchiveName == "" || receipt.Records != 1 || !receipt.Portable {
		t.Fatalf("receipt=%+v", receipt)
	}
	if _, err := os.Stat(filepath.Join(destination, receipt.ArchiveName)); err != nil {
		t.Fatal(err)
	}
	if approvals != 2 {
		t.Fatalf("approval prompts=%d, want 2", approvals)
	}
}

func TestApplicationExposesOptInRecordWriters(t *testing.T) {
	workspace := t.TempDir()
	app, err := Open(context.Background(), config.Settings{
		Workspace: workspace, StateDir: t.TempDir(), CatalogRoots: []string{"catalog"},
		EnableIntentionWrites: true, EnableReportImport: true,
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	found := map[string]bool{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		found[tool.Name] = true
	}
	for _, name := range []string{"project_records", "checkpoint_write", "memo_write", "check_report_import"} {
		if !found[name] {
			t.Fatalf("%s not exposed: %v", name, found)
		}
	}
	checkpoint, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "checkpoint_write", Arguments: map[string]any{"mode": "create", "goal": "test records"}})
	if err != nil || checkpoint.IsError {
		t.Fatalf("checkpoint write result=%+v error=%v", checkpoint, err)
	}
	query, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "project_records", Arguments: map[string]any{"mode": "list", "kind": "checkpoint"}})
	if err != nil || query.IsError || query.StructuredContent == nil {
		t.Fatalf("record query result=%+v error=%v", query, err)
	}
	search, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "project_records", Arguments: map[string]any{"mode": "search", "kind": "checkpoint", "query": "test records", "item_limit": 5, "byte_limit": 4096}})
	if err != nil || search.IsError || search.StructuredContent == nil {
		t.Fatalf("record search result=%+v error=%v", search, err)
	}
}

func TestEmptyCatalogAndSearchReturnExactEmptyResults(t *testing.T) {
	workspace := t.TempDir()
	app, err := Open(context.Background(), config.Settings{
		Workspace: workspace, StateDir: t.TempDir(), CatalogRoots: []string{"catalog"},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := mcpserver.New("test", app.MCPOptions()).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	requests := []struct {
		name      string
		arguments map[string]any
	}{
		{name: "catalog_query", arguments: map[string]any{"mode": "list"}},
		{name: "workspace_search", arguments: map[string]any{"mode": "filename", "pattern": ""}},
		{name: "project_records", arguments: map[string]any{"mode": "list"}},
	}
	for _, request := range requests {
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: request.name, Arguments: request.arguments})
		if err != nil {
			t.Fatalf("%s: %v", request.name, err)
		}
		if result.IsError || result.StructuredContent == nil {
			t.Fatalf("%s returned %+v", request.name, result)
		}
		payload, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Status string `json:"status"`
			Items  []any  `json:"items"`
			Counts struct {
				Matched  *uint64 `json:"matched"`
				Relation string  `json:"relation"`
			} `json:"counts"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Status != "ok" || len(envelope.Items) != 0 || envelope.Counts.Matched == nil || *envelope.Counts.Matched != 0 || envelope.Counts.Relation != "exact" {
			t.Fatalf("%s empty response=%s", request.name, payload)
		}
	}
}

func TestApplicationRejectsStateInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	_, err := Open(context.Background(), config.Settings{
		Workspace: workspace, StateDir: filepath.Join(workspace, ".state"), CatalogRoots: []string{"catalog"},
	}, "test")
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("error=%v, want state boundary rejection", err)
	}
}

func TestApplicationCreatesSeparateCacheAndDurableDatabases(t *testing.T) {
	workspace := t.TempDir()
	state := t.TempDir()
	app, err := Open(context.Background(), config.Settings{Workspace: workspace, StateDir: state, CatalogRoots: []string{"catalog"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"repoplane.db", "records.db"} {
		if info, err := os.Stat(filepath.Join(state, name)); err != nil || info.IsDir() {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
}

func TestPathInsideTreatsDifferentWindowsVolumesAsOutside(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows volume semantics")
	}
	inside, err := pathInside(`C:\workspace`, `D:\state`)
	if err != nil {
		t.Fatal(err)
	}
	if inside {
		t.Fatal("a path on another volume must be outside the workspace")
	}
}
