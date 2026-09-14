// Package schemaexport renders the MCP tool contracts for source control.
package schemaexport

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"repoplane/internal/catalog"
	"repoplane/internal/dataquery"
	"repoplane/internal/mcpserver"
	"repoplane/internal/pathfacts"
	"repoplane/internal/search"
)

type document struct {
	Schema string       `json:"$schema"`
	ID     string       `json:"$id"`
	Tools  []toolSchema `json:"tools"`
}

type toolSchema struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	InputSchema  any    `json:"input_schema"`
	OutputSchema any    `json:"output_schema"`
}

func Generate(ctx context.Context) ([]byte, error) {
	server := mcpserver.New("schema-export", mcpserver.Options{
		Catalog: &catalog.Service{}, Search: &search.Service{},
		PathFacts: &pathfacts.Service{}, DataQuery: &dataquery.Service{},
	})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect schema server: %w", err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "schema-export", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect schema client: %w", err)
	}
	defer session.Close()

	document := document{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		ID:     "https://repoplane.local/schemas/tools.v1.json",
		Tools:  make([]toolSchema, 0, 4),
	}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		document.Tools = append(document.Tools, toolSchema{
			Name: tool.Name, Description: tool.Description,
			InputSchema: tool.InputSchema, OutputSchema: tool.OutputSchema,
		})
	}
	sort.Slice(document.Tools, func(i, j int) bool { return document.Tools[i].Name < document.Tools[j].Name })
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}
