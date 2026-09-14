package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"repoplane/internal/contracts"
	"repoplane/internal/cursor"
	"repoplane/internal/dataquery"
	"repoplane/internal/records"
	"repoplane/internal/runtimeaccess"
	"repoplane/internal/runtimeconfig"
	"repoplane/internal/store"
	"repoplane/internal/workspace"
)

type testRuntimeConfig struct{}

func (testRuntimeConfig) Status(context.Context) (runtimeconfig.Response, error) {
	return runtimeconfig.Response{}, nil
}
func (testRuntimeConfig) Apply(context.Context, runtimeconfig.Request) (runtimeconfig.Response, error) {
	return runtimeconfig.Response{}, nil
}

func TestRuntimeConfigurationIsListedButUnavailableOnHTTPAuthority(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := New("test", Options{RuntimeAccess: runtimeaccess.New(nil, false, runtimeaccess.Initial{}), RuntimeConfig: testRuntimeConfig{}}).Connect(ctx, serverTransport, nil)
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
	found := false
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		found = found || tool.Name == ToolRuntimeConfig
	}
	if !found {
		t.Fatal("runtime_config is missing from stable discovery")
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolRuntimeConfig, Arguments: map[string]any{"action": "status"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("HTTP-authority runtime configuration succeeded: %+v", result)
	}
}

func TestServerNegotiatesInMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := New("test").Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "repoplane-test", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer clientSession.Close()

	var names []string
	for tool, err := range clientSession.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		names = append(names, tool.Name)
	}
	if len(names) != 0 {
		t.Fatalf("initial server exposed tools %v, want none", names)
	}
}

func TestPublicErrorUsesStableSanitizedCodes(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("host detail: %w", workspace.ErrEscape), "workspace_escape"},
		{cursor.ErrInvalid, "cursor_invalid"},
		{cursor.ErrExpired, "cursor_expired"},
		{dataquery.ErrSourceChanged, "source_changed"},
		{fs.ErrNotExist, "source_not_found"},
		{store.ErrNotFound, "record_not_found"},
		{store.ErrConflict, "revision_conflict"},
		{records.ErrReportInvalid, "report_invalid"},
		{runtimeaccess.ErrDisabled, "runtime_access_unavailable"},
		{runtimeaccess.ErrDeclined, "permission_denied"},
		{runtimeaccess.ErrPending, "runtime_approval_invalid"},
		{runtimeconfig.ErrUnavailable, "runtime_configuration_unavailable"},
		{runtimeconfig.ErrBusy, "runtime_configuration_busy"},
		{contracts.ErrLimitExceeded, "limit_exceeded"},
		{errors.New("ref is required"), "invalid_argument"},
		{errors.New("database exploded at a host path"), "internal_error"},
	}
	for _, test := range tests {
		if got := publicError(test.err).Error(); got != test.want {
			t.Errorf("publicError(%v)=%q, want %q", test.err, got, test.want)
		}
	}
}

func TestRequiredScopeCoversEveryPublicTool(t *testing.T) {
	reads, writes, imports, runners, state := ToolNames()
	tests := []struct {
		names []string
		scope string
	}{
		{reads, ScopeRead},
		{writes, ScopeIntentWrite},
		{imports, ScopeReportImport},
		{runners, ScopeRunnerExecute},
		{state, ScopeStateExport},
	}
	seen := map[string]bool{}
	for _, test := range tests {
		for _, name := range test.names {
			if seen[name] {
				t.Fatalf("duplicate public tool %q", name)
			}
			seen[name] = true
			if got, ok := RequiredScope(name); !ok || got != test.scope {
				t.Fatalf("RequiredScope(%q)=(%q,%v), want (%q,true)", name, got, ok, test.scope)
			}
		}
	}
	if len(seen) != 14 {
		t.Fatalf("covered tools=%d, want 14", len(seen))
	}
	if _, ok := RequiredScope("unknown"); ok {
		t.Fatal("unknown tool did not fail closed")
	}
}
