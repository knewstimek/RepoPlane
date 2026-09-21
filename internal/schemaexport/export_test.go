package schemaexport

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInspectSchemaExplainsStoredReferencesWithoutREADME(t *testing.T) {
	generated, err := Generate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var schema document
	if err := json.Unmarshal(generated, &schema); err != nil {
		t.Fatal(err)
	}
	for _, tool := range schema.Tools {
		if tool.Name != "run_inspect" {
			continue
		}
		for _, phrase := range []string{"runtime_config(status).configuration.state_dir[0]", "HTTP", "response_view=bytes"} {
			if !strings.Contains(tool.Description, phrase) {
				t.Fatalf("run_inspect description omits %q", phrase)
			}
		}
		input := tool.InputSchema.(map[string]any)["properties"].(map[string]any)
		view := input["response_view"].(map[string]any)["description"].(string)
		if !strings.Contains(view, "ref default") || !strings.Contains(view, "bytes") {
			t.Fatalf("response_view is not self-describing: %q", view)
		}
		output := tool.OutputSchema.(map[string]any)["properties"].(map[string]any)
		stream := output["stream"].(map[string]any)["properties"].(map[string]any)
		file := stream["file_ref"].(map[string]any)["description"].(string)
		if !strings.Contains(file, "strip state:") || !strings.Contains(file, "local only") {
			t.Fatalf("file_ref is not self-describing: %q", file)
		}
		return
	}
	t.Fatal("run_inspect is absent")
}

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

func TestCommonInputChoicesAreExposed(t *testing.T) {
	generated, err := Generate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var parsed document
	if err := json.Unmarshal(generated, &parsed); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string][]string{
		"catalog_query":    {"mode": {"search", "get", "list", "audit", "status"}},
		"workspace_search": {"mode": {"filename", "exact", "regex", "git_history", "symbol"}, "ignored": {"exclude", "include"}},
		"data_query":       {"mode": {"text_range", "jsonl", "json", "delimited", "log"}},
		"project_records":  {"mode": {"search", "list", "get"}},
	}
	for _, tool := range parsed.Tools {
		fields, ok := want[tool.Name]
		if !ok {
			continue
		}
		properties := tool.InputSchema.(map[string]any)["properties"].(map[string]any)
		for field, values := range fields {
			property := properties[field].(map[string]any)
			got := property["enum"].([]any)
			if len(got) != len(values) {
				t.Fatalf("%s.%s enum=%v, want %v", tool.Name, field, got, values)
			}
			for i, value := range values {
				if got[i] != value {
					t.Fatalf("%s.%s enum=%v, want %v", tool.Name, field, got, values)
				}
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
