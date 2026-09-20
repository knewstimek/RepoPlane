package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"repoplane/internal/catalog"
	"repoplane/internal/dataquery"
	"repoplane/internal/memorybackup"
	"repoplane/internal/pathfacts"
	"repoplane/internal/records"
	"repoplane/internal/runner"
	"repoplane/internal/runtimeaccess"
	"repoplane/internal/search"
	"repoplane/internal/store"
)

type catalogToolboxStub struct {
	calls int
	input catalog.QueryRequest
}

type memoErrorStub struct{ err error }

func (s memoErrorStub) WriteMemo(context.Context, records.MemoRequest) (records.MutationResponse, error) {
	return records.MutationResponse{}, s.err
}

func (s *catalogToolboxStub) Query(_ context.Context, input catalog.QueryRequest) (catalog.QueryResponse, error) {
	s.calls++
	s.input = input
	return catalog.QueryResponse{Status: "ok"}, nil
}

func fullRegistryOptions() Options {
	return Options{
		Catalog: &catalog.Service{}, Search: &search.Service{}, PathFacts: &pathfacts.Service{},
		DataQuery: &dataquery.Service{}, Records: &records.Service{}, CheckpointWriter: &records.Service{},
		MemoWriter: &records.Service{}, ReportImporter: &records.Service{}, Runner: &runner.Service{},
		RuntimeAccess: runtimeaccess.New(nil, false, runtimeaccess.Initial{}), RuntimeConfig: testRuntimeConfig{},
		MemoryBackup: &memorybackup.Service{},
	}
}

func TestUsageObserverCountsTypedAndToolboxOperationCalls(t *testing.T) {
	for _, surface := range []string{SurfaceTypedV1, SurfaceToolboxV1} {
		t.Run(surface, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stub := &catalogToolboxStub{}
			var events []store.UsageEvent
			session := connectTestClient(t, ctx, Options{Surface: surface, Catalog: stub,
				ObserveUsage: func(_ context.Context, event store.UsageEvent) { events = append(events, event) }})
			defer session.Close()
			if surface == SurfaceTypedV1 {
				if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolCatalogQuery,
					Arguments: map[string]any{"mode": "list"}}); err != nil {
					t.Fatal(err)
				}
			} else {
				described := callToolbox(t, ctx, session, ToolboxRead,
					map[string]any{"action": "describe", "operation": ToolCatalogQuery})
				callToolbox(t, ctx, session, ToolboxRead, map[string]any{"action": "call",
					"operation": ToolCatalogQuery, "schema_handle": described.SchemaHandle,
					"arguments": map[string]any{"mode": "list"}})
			}
			if stub.calls != 1 || len(events) != 1 || events[0].Tool != ToolCatalogQuery ||
				events[0].Outcome != "ok" || events[0].RequestBytes == 0 || events[0].ResponseBytes == 0 {
				t.Fatalf("calls=%d events=%+v", stub.calls, events)
			}
		})
	}
}

func TestMemoWriteReturnsStructuredMutationFailureToMCPClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session := connectTestClient(t, ctx, Options{
		Surface: SurfaceTypedV1, MemoWriter: memoErrorStub{err: records.ErrInvalidTransition},
		RuntimeAccess: runtimeaccess.New(nil, true, runtimeaccess.Initial{IntentWrite: true}),
	})
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolMemoWrite, Arguments: map[string]any{
		"mode": "update", "id": "memo_example", "expected_revision": 2,
		"memo_kind": "decision", "content": "updated decision",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("memo_write succeeded: %+v", result)
	}
	if len(result.Content) != 1 {
		t.Fatalf("error content=%+v", result.Content)
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("error content type=%T", result.Content[0])
	}
	failure := decodePublicFailure(t, errors.New(content.Text))
	if failure.Code != "invalid_transition" || failure.MutationState != "not_applied" || failure.CorrelationID == "" {
		t.Fatalf("failure=%+v", failure)
	}
}

func TestMemoWriteReturnsHostFactValidationFailureToMCPClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session := connectTestClient(t, ctx, Options{
		Surface: SurfaceTypedV1,
		MemoWriter: memoErrorStub{err: fmt.Errorf("%w: %w", records.ErrInvalidArgument,
			&records.ValidationError{Field: "topic_key", Reason: "is not valid for host_fact"})},
		RuntimeAccess: runtimeaccess.New(nil, true, runtimeaccess.Initial{IntentWrite: true}),
	})
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolMemoWrite, Arguments: map[string]any{
		"mode": "create", "memo_kind": "host_fact", "scope": "operations/hosts", "topic_key": "host-a",
		"host": map[string]any{
			"alias": "host-a", "role": "worker", "os": "linux", "tier": "production",
			"services": []string{"worker"}, "paths": nil, "confirmed_at": "2026-09-20T00:00:00Z",
		},
		"invalidation_condition": "the host changes",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("memo_write result=%+v", result)
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("error content type=%T", result.Content[0])
	}
	failure := decodePublicFailure(t, errors.New(content.Text))
	if failure.Code != "invalid_argument" || failure.Message != "validation failed: topic_key is not valid for host_fact" || failure.MutationState != "not_applied" || failure.CorrelationID == "" {
		t.Fatalf("failure=%+v", failure)
	}
}

func TestOperationRegistryCoversTypedToolsExactlyOnce(t *testing.T) {
	operations := operationRegistry(fullRegistryOptions())
	if len(operations) != 14 {
		t.Fatalf("operations=%d, want 14", len(operations))
	}
	seen := make(map[string]bool, len(operations))
	toolboxes := make(map[string]int)
	for _, operation := range operations {
		if seen[operation.name] {
			t.Fatalf("duplicate operation %q", operation.name)
		}
		seen[operation.name] = true
		toolboxes[operation.toolbox]++
		if scope, ok := RequiredScope(operation.name); !ok || scope != operation.scope {
			t.Fatalf("operation %s scope=(%q,%v), want %q", operation.name, scope, ok, operation.scope)
		}
		if scope, ok := RequiredScope(operation.toolbox); !ok || scope != operation.scope {
			t.Fatalf("toolbox %s scope=(%q,%v), want %q", operation.toolbox, scope, ok, operation.scope)
		}
	}
	want := map[string]int{ToolboxRead: 6, ToolboxWrite: 2, ToolboxImport: 1, ToolboxRunner: 3, ToolboxState: 2}
	for toolbox, count := range want {
		if toolboxes[toolbox] != count {
			t.Fatalf("%s operations=%d, want %d", toolbox, toolboxes[toolbox], count)
		}
	}
}

func TestOperationContractsHaveDeterministicDistinctHandles(t *testing.T) {
	seen := map[string]string{}
	for _, operation := range operationRegistry(fullRegistryOptions()) {
		handle, compact, complete, err := contractsFor(operation)
		if err != nil {
			t.Fatal(err)
		}
		again, _, _, err := contractsFor(operation)
		if err != nil || again != handle {
			t.Fatalf("%s handle changed: %q %q err=%v", operation.name, handle, again, err)
		}
		if previous, exists := seen[handle]; exists {
			t.Fatalf("operations %s and %s share handle %s", previous, operation.name, handle)
		}
		seen[handle] = operation.name
		if compact.OutputSchema != nil || complete.OutputSchema == nil || compact.InputSchema == nil {
			t.Fatalf("%s compact/complete contract mismatch", operation.name)
		}
	}
}

func TestToolboxSurfaceIsFixedAndUnderInputBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session := connectTestClient(t, ctx, Options{Surface: SurfaceToolboxV1, Catalog: &catalog.Service{}, Search: &search.Service{}, PathFacts: &pathfacts.Service{}, DataQuery: &dataquery.Service{}, Records: &records.Service{}, CheckpointWriter: &records.Service{}, MemoWriter: &records.Service{}, ReportImporter: &records.Service{}, Runner: &runner.Service{}, RuntimeAccess: runtimeaccess.New(nil, false, runtimeaccess.Initial{}), RuntimeConfig: testRuntimeConfig{}, MemoryBackup: &memorybackup.Service{}})
	defer session.Close()

	var names []string
	bytesUsed := 0
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
		encoded, err := json.Marshal(struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Input       any    `json:"input_schema"`
		}{tool.Name, tool.Description, tool.InputSchema})
		if err != nil {
			t.Fatal(err)
		}
		bytesUsed += len(encoded)
	}
	want := ToolboxNames()
	sort.Strings(want)
	if encoded, _ := json.Marshal(names); !bytes.Equal(encoded, mustJSON(t, want)) {
		t.Fatalf("toolbox names=%v, want %v", names, want)
	}
	if bytesUsed > 5*1024 {
		t.Fatalf("toolbox discovery input bytes=%d, budget=%d", bytesUsed, 5*1024)
	}
}

func TestToolboxDescribeCallAndHandleReuse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stub := &catalogToolboxStub{}
	var audited []string
	session := connectTestClient(t, ctx, Options{Surface: SurfaceToolboxV1, Catalog: stub, AuditOperation: func(_ context.Context, toolbox, operation string) error {
		audited = append(audited, toolbox+":"+operation)
		return nil
	}})
	defer session.Close()

	described := callToolbox(t, ctx, session, ToolboxRead, map[string]any{"action": "describe", "operation": ToolCatalogQuery})
	if described.Status != "ok" || described.Contract == nil || described.SchemaHandle == "" {
		t.Fatalf("describe=%+v", described)
	}
	unchanged := callToolbox(t, ctx, session, ToolboxRead, map[string]any{"action": "describe", "operation": ToolCatalogQuery, "known_schema_handle": described.SchemaHandle})
	if unchanged.Status != "unchanged" || unchanged.Contract != nil {
		t.Fatalf("unchanged=%+v", unchanged)
	}
	changed := callToolbox(t, ctx, session, ToolboxRead, map[string]any{"action": "call", "operation": ToolCatalogQuery, "schema_handle": "schema:wrong@sha256:0", "arguments": map[string]any{"mode": "list"}})
	if changed.Status != "schema_changed" || changed.Contract == nil || stub.calls != 0 {
		t.Fatalf("schema_changed=%+v calls=%d", changed, stub.calls)
	}
	called := callToolbox(t, ctx, session, ToolboxRead, map[string]any{"action": "call", "operation": ToolCatalogQuery, "schema_handle": described.SchemaHandle, "arguments": map[string]any{"mode": "list", "item_limit": 3}})
	if called.Status != "ok" || stub.calls != 1 || stub.input.Mode != "list" || stub.input.ItemLimit != 3 {
		t.Fatalf("call=%+v calls=%d input=%+v", called, stub.calls, stub.input)
	}
	before := listToolboxBytes(t, ctx, session)
	_ = callToolbox(t, ctx, session, ToolboxRead, map[string]any{"action": "describe", "operation": ToolCatalogQuery})
	after := listToolboxBytes(t, ctx, session)
	if !bytes.Equal(before, after) {
		t.Fatal("tools/list changed after toolbox use")
	}
	if len(audited) != 5 {
		t.Fatalf("audited operations=%v", audited)
	}
	for _, value := range audited {
		if value != ToolboxRead+":"+ToolCatalogQuery {
			t.Fatalf("audit identity=%q", value)
		}
	}
}

func TestToolboxCallRejectsUnknownOperationArguments(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stub := &catalogToolboxStub{}
	session := connectTestClient(t, ctx, Options{Surface: SurfaceToolboxV1, Catalog: stub})
	defer session.Close()
	described := callToolbox(t, ctx, session, ToolboxRead, map[string]any{"action": "describe", "operation": ToolCatalogQuery})
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolboxRead, Arguments: map[string]any{
		"action": "call", "operation": ToolCatalogQuery, "schema_handle": described.SchemaHandle,
		"arguments": map[string]any{"mode": "list", "unknown": true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || stub.calls != 0 {
		t.Fatalf("unknown argument result=%+v calls=%d", result, stub.calls)
	}
}

func TestToolboxChecksResolvedOperationBeforeAudit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stub := &catalogToolboxStub{}
	audits := 0
	session := connectTestClient(t, ctx, Options{
		Surface: SurfaceToolboxV1,
		Catalog: stub,
		Authorize: func(_ context.Context, name string) error {
			if name == ToolCatalogQuery {
				return errors.New("denied")
			}
			return nil
		},
		AuditOperation: func(context.Context, string, string) error {
			audits++
			return nil
		},
	})
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolboxRead, Arguments: map[string]any{
		"action": "describe", "operation": ToolCatalogQuery,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || audits != 0 || stub.calls != 0 {
		t.Fatalf("result=%+v audits=%d calls=%d", result, audits, stub.calls)
	}
}

func TestApprovalMessageNamesResolvedOperation(t *testing.T) {
	message := approvalMessage(ToolMemoWrite, "approve durable write")
	if message != "Operation memo_write: approve durable write" {
		t.Fatalf("message=%q", message)
	}
}

func TestToolboxArgumentBoundsRejectExcessiveDepth(t *testing.T) {
	raw := append(bytes.Repeat([]byte{'['}, 65), bytes.Repeat([]byte{']'}, 65)...)
	if err := validateJSONBounds(raw); err == nil {
		t.Fatal("excessive JSON depth succeeded")
	}
}

func connectTestClient(t *testing.T, ctx context.Context, options Options) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := New("test", options).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "toolbox-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func callToolbox(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, arguments map[string]any) toolboxResponse {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("toolbox call failed: %+v", result)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output toolboxResponse
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	return output
}

func listToolboxBytes(t *testing.T, ctx context.Context, session *mcp.ClientSession) []byte {
	t.Helper()
	var tools []*mcp.Tool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		tools = append(tools, tool)
	}
	encoded, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
