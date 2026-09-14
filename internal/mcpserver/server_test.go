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
	"repoplane/internal/workspace"
)

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
