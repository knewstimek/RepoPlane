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
	"repoplane/internal/memorybackup"
	"repoplane/internal/pathfacts"
	"repoplane/internal/records"
	"repoplane/internal/runner"
	"repoplane/internal/runtimeaccess"
	"repoplane/internal/runtimeconfig"
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

type footprintDocument struct {
	SchemaVersion       string         `json:"schema_version"`
	SourceSchema        string         `json:"source_schema"`
	ToolboxSourceSchema string         `json:"toolbox_source_schema,omitempty"`
	Measurement         string         `json:"measurement"`
	Sets                []footprintSet `json:"sets"`
}

type footprintSet struct {
	Name                      string `json:"name"`
	ToolCount                 int    `json:"tool_count"`
	CompleteContractBytes     int    `json:"complete_contract_bytes"`
	NameDescriptionInputBytes int    `json:"name_description_input_bytes"`
	InputSchemaBytes          int    `json:"input_schema_bytes"`
	OutputSchemaBytes         int    `json:"output_schema_bytes"`
}

type exposureCandidate struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

type configurationSchemaController struct{}

func (configurationSchemaController) Status(context.Context) (runtimeconfig.Response, error) {
	return runtimeconfig.Response{}, nil
}
func (configurationSchemaController) Apply(context.Context, runtimeconfig.Request) (runtimeconfig.Response, error) {
	return runtimeconfig.Response{}, nil
}

func Generate(ctx context.Context) ([]byte, error) {
	return generate(ctx, mcpserver.SurfaceTypedV1, "https://repoplane.local/schemas/tools.v1.json", 14)
}

func GenerateToolboxes(ctx context.Context) ([]byte, error) {
	return generate(ctx, mcpserver.SurfaceToolboxV1, "https://repoplane.local/schemas/toolboxes.v1.json", 5)
}

func generate(ctx context.Context, surface, id string, capacity int) ([]byte, error) {
	server := mcpserver.New("schema-export", mcpserver.Options{
		Surface: surface,
		Catalog: &catalog.Service{}, Search: &search.Service{},
		PathFacts: &pathfacts.Service{}, DataQuery: &dataquery.Service{}, Records: &records.Service{},
		CheckpointWriter: &records.Service{}, MemoWriter: &records.Service{}, ReportImporter: &records.Service{},
		Runner:        &runner.Service{},
		RuntimeAccess: runtimeaccess.New(nil, false, runtimeaccess.Initial{}),
		RuntimeConfig: configurationSchemaController{},
		MemoryBackup:  &memorybackup.Service{},
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
		ID:     id,
		Tools:  make([]toolSchema, 0, capacity),
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

// GenerateFootprint reports serialized contract bytes without claiming that a
// particular client, tokenizer, model, or code path exposes those bytes.
func GenerateFootprint(toolDocument []byte, toolboxDocuments ...[]byte) ([]byte, error) {
	var parsed document
	if err := json.Unmarshal(toolDocument, &parsed); err != nil {
		return nil, fmt.Errorf("decode tool schema document: %w", err)
	}
	groups := map[string]map[string]bool{
		"read":   {},
		"writes": {},
		"runner": {},
		"state":  {},
		"all":    {},
	}
	var toolboxParsed document
	if len(toolboxDocuments) > 0 {
		if err := json.Unmarshal(toolboxDocuments[0], &toolboxParsed); err != nil {
			return nil, fmt.Errorf("decode toolbox schema document: %w", err)
		}
		groups["toolbox"] = map[string]bool{}
		for _, tool := range toolboxParsed.Tools {
			groups["toolbox"][tool.Name] = true
		}
	}
	reads, writes, imports, runners, state := mcpserver.ToolNames()
	for _, name := range reads {
		groups["read"][name] = true
	}
	for _, name := range append(writes, imports...) {
		groups["writes"][name] = true
	}
	for _, name := range runners {
		groups["runner"][name] = true
	}
	for _, name := range state {
		groups["state"][name] = true
	}
	for _, tool := range parsed.Tools {
		groups["all"][tool.Name] = true
	}
	report := footprintDocument{
		SchemaVersion: "tool-footprint.v1", SourceSchema: "schemas/tools.v1.json",
		Measurement: "compact serialized JSON bytes; not observed model tokens or client exposure",
		Sets:        make([]footprintSet, 0, 6),
	}
	groupOrder := []string{"read", "writes", "runner", "state", "all"}
	if len(toolboxDocuments) > 0 {
		report.ToolboxSourceSchema = "schemas/toolboxes.v1.json"
		groupOrder = append(groupOrder, "toolbox")
	}
	for _, group := range groupOrder {
		set := footprintSet{Name: group}
		tools := parsed.Tools
		if group == "toolbox" {
			tools = toolboxParsed.Tools
		}
		for _, tool := range tools {
			if !groups[group][tool.Name] {
				continue
			}
			set.ToolCount++
			complete, err := json.Marshal(tool)
			if err != nil {
				return nil, err
			}
			candidate, err := json.Marshal(exposureCandidate{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
			if err != nil {
				return nil, err
			}
			input, err := json.Marshal(tool.InputSchema)
			if err != nil {
				return nil, err
			}
			output, err := json.Marshal(tool.OutputSchema)
			if err != nil {
				return nil, err
			}
			set.CompleteContractBytes += len(complete)
			set.NameDescriptionInputBytes += len(candidate)
			set.InputSchemaBytes += len(input)
			set.OutputSchemaBytes += len(output)
		}
		report.Sets = append(report.Sets, set)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}
