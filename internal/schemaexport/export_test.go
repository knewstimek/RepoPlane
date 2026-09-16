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

func TestGeneratedFootprintIsCurrentAndSeparatesMeasurements(t *testing.T) {
	generated, err := Generate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	toolboxes, err := GenerateToolboxes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	footprint, err := GenerateFootprint(generated, toolboxes)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(filepath.Join("..", "..", "schemas", "tool-footprint.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(footprint, committed) {
		t.Fatal("schemas/tool-footprint.v1.json is stale; run go generate ./internal/mcpserver")
	}
	var report footprintDocument
	if err := json.Unmarshal(footprint, &report); err != nil {
		t.Fatal(err)
	}
	wantCounts := map[string]int{"read": 6, "writes": 3, "runner": 3, "state": 2, "all": 14, "toolbox": 5}
	for _, set := range report.Sets {
		if set.ToolCount != wantCounts[set.Name] {
			t.Fatalf("%s tool_count=%d, want %d", set.Name, set.ToolCount, wantCounts[set.Name])
		}
		if set.NameDescriptionInputBytes >= set.CompleteContractBytes {
			t.Fatalf("%s does not separate candidate exposure from the complete contract: %+v", set.Name, set)
		}
		if set.Name == "toolbox" && set.NameDescriptionInputBytes > 5*1024 {
			t.Fatalf("toolbox exposure bytes=%d, budget=%d", set.NameDescriptionInputBytes, 5*1024)
		}
	}
}

func TestGeneratedToolboxSchemasAreCurrent(t *testing.T) {
	generated, err := GenerateToolboxes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(filepath.Join("..", "..", "schemas", "toolboxes.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, committed) {
		t.Fatal("schemas/toolboxes.v1.json is stale; run go generate ./internal/mcpserver")
	}
	var parsed document
	if err := json.Unmarshal(generated, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tools) != 5 {
		t.Fatalf("toolbox count=%d, want 5", len(parsed.Tools))
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
		if tool.Name != "catalog_query" && tool.Name != "workspace_search" && tool.Name != "data_query" && tool.Name != "project_records" {
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

func TestCompactToolSchemaFootprintStaysBounded(t *testing.T) {
	generated, err := Generate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var parsed document
	if err := json.Unmarshal(generated, &parsed); err != nil {
		t.Fatal(err)
	}
	const maximumAllTools = 35 * 1024
	const maximumRunnerTools = 5500
	total, runner := 0, 0
	for _, tool := range parsed.Tools {
		compact, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		total += len(compact)
		if tool.Name == "run_prepare" || tool.Name == "run_execute" || tool.Name == "run_inspect" {
			runner += len(compact)
		}
	}
	if total > maximumAllTools {
		t.Fatalf("compact MCP tool schemas use %d bytes, budget %d", total, maximumAllTools)
	}
	if runner > maximumRunnerTools {
		t.Fatalf("compact Runner tool schemas use %d bytes, budget %d", runner, maximumRunnerTools)
	}
}
