package schemaexport

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedToolSchemasAreCurrent(t *testing.T) {
	generated, err := Generate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(filepath.Join("..", "..", "schemas", "tools.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, committed) {
		t.Fatal("schemas/tools.v1.json is stale; run go generate ./internal/mcpserver")
	}
}

func TestCursorCapableToolSchemasAllowCursorOnlyRequests(t *testing.T) {
	generated, err := Generate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var parsed document
	if err := json.Unmarshal(generated, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, tool := range parsed.Tools {
		if tool.Name != "catalog_query" && tool.Name != "workspace_search" && tool.Name != "data_query" {
			continue
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("%s input schema type %T", tool.Name, tool.InputSchema)
		}
		if required, exists := schema["required"]; exists {
			if values, ok := required.([]any); !ok || len(values) != 0 {
				t.Fatalf("%s cursor request is blocked by required=%v", tool.Name, required)
			}
		}
	}
}
