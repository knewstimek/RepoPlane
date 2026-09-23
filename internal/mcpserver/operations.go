package mcpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"repoplane/internal/catalog"
	"repoplane/internal/contracts"
	"repoplane/internal/dataquery"
	"repoplane/internal/memorybackup"
	"repoplane/internal/pathfacts"
	"repoplane/internal/records"
	"repoplane/internal/runner"
	"repoplane/internal/runtimeaccess"
	"repoplane/internal/runtimeconfig"
	"repoplane/internal/search"
	"repoplane/internal/store"
	"repoplane/internal/workspace"
)

const (
	SurfaceTypedV1   = "typed.v1"
	SurfaceToolboxV1 = "toolbox.v1"

	ToolboxRead   = "repoplane_read"
	ToolboxWrite  = "repoplane_write"
	ToolboxImport = "repoplane_import"
	ToolboxRunner = "repoplane_runner"
	ToolboxState  = "repoplane_state"

	toolboxContractVersion = "operation-contract.v1"
	maxToolboxArguments    = 1 << 20
)

type operationSpec struct {
	name         string
	toolbox      string
	description  string
	scope        string
	sideEffect   string
	approval     string
	inputSchema  any
	outputSchema any
	register     func(*mcp.Server)
	invoke       func(context.Context, *mcp.CallToolRequest, json.RawMessage) (*mcp.CallToolResult, any, error)
	setObserver  func(func(context.Context, store.UsageEvent))
}

type toolboxRequest struct {
	Action            string          `json:"action"`
	Operation         string          `json:"operation"`
	Detail            string          `json:"detail,omitempty"`
	KnownSchemaHandle string          `json:"known_schema_handle,omitempty"`
	SchemaHandle      string          `json:"schema_handle,omitempty"`
	Arguments         json.RawMessage `json:"arguments,omitempty"`
}

type operationContract struct {
	SchemaVersion string `json:"schema_version"`
	Description   string `json:"description"`
	InputSchema   any    `json:"input_schema"`
	OutputSchema  any    `json:"output_schema,omitempty"`
	ResultSummary string `json:"result_summary"`
	Scope         string `json:"scope"`
	SideEffect    string `json:"side_effect"`
	Approval      string `json:"approval"`
	Retry         string `json:"retry"`
}

type toolboxResponse struct {
	Status       string             `json:"status"`
	Operation    string             `json:"operation"`
	SchemaHandle string             `json:"schema_handle"`
	Contract     *operationContract `json:"contract,omitempty"`
	Result       any                `json:"result,omitempty"`
}

// addInputChoices exposes choices already enforced by the services to MCP clients.
// Cursor-capable tools keep mode optional so a cursor-only request stays valid.
func addInputChoices(name string, schema *jsonschema.Schema) {
	choices := map[string]map[string][]string{
		ToolCatalogQuery: {"mode": {"search", "get", "list", "audit", "status"}},
		ToolWorkspaceSearch: {
			"mode":    {"filename", "exact", "regex", "git_history", "symbol"},
			"ignored": {"exclude", "include"}, "generated": {"exclude", "include"},
			"vendor":         {"exclude", "include"},
			"encoding":       {"utf-8", "cp949", "euc-kr"},
			"match_kind":     {"all", "commit", "path", "diff"},
			"pattern_syntax": {"exact", "regex"},
		},
		ToolDataQuery:       {"mode": {"text_range", "jsonl", "json", "delimited", "log"}},
		ToolProjectRecords:  {"mode": {"search", "list", "get", "get_topic", "resume"}, "match_mode": {"any", "all"}, "response_view": {"brief", "discovery", "full"}},
		ToolCheckpointWrite: {"mode": {"create", "update", "supersede"}, "response_view": {"full", "receipt"}},
		ToolMemoWrite: {
			"mode":   {"create", "update", "supersede"},
			"source": {"user_asserted", "llm_proposed"}, "response_view": {"full", "receipt"},
		},
		ToolCheckReportImport: {"response_view": {"full", "receipt"}},
		ToolRunPrepare:        {"cache_mode": {"auto", "bypass"}},
	}
	for field, values := range choices[name] {
		property := schema.Properties[field]
		if property == nil {
			panic(fmt.Sprintf("operation %s missing input field %s", name, field))
		}
		property.Enum = make([]any, len(values))
		for i, value := range values {
			property.Enum[i] = value
		}
	}
}

func newOperation[In, Out any](name, toolbox, description, scope, sideEffect, approval string, annotations *mcp.ToolAnnotations, handler mcp.ToolHandlerFor[In, Out]) operationSpec {
	inputSchema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("operation %s input schema: %v", name, err))
	}
	outputSchema, err := jsonschema.For[Out](nil)
	if err != nil {
		panic(fmt.Sprintf("operation %s output schema: %v", name, err))
	}
	addInputChoices(name, inputSchema)
	tool := &mcp.Tool{Name: name, Description: description, InputSchema: inputSchema, Annotations: annotations}
	var observe func(context.Context, store.UsageEvent)
	observed := func(ctx context.Context, request *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error) {
		if observe == nil {
			return handler(ctx, request, input)
		}
		started := time.Now()
		result, output, err := handler(ctx, request, input)
		duration := time.Since(started)
		inputJSON, _ := json.Marshal(input)
		outputJSON, _ := json.Marshal(output)
		outcome := "ok"
		if err != nil {
			outcome = "error"
			outputJSON = nil
		} else if result != nil {
			outcome = "approval"
			outputJSON, _ = json.Marshal(result)
		} else if execution, ok := any(output).(runner.ExecuteResponse); ok && execution.State == "reused" {
			outcome = "reused"
		}
		observe(ctx, store.UsageEvent{At: time.Now().UTC(), Tool: name, Outcome: outcome,
			RequestBytes: uint64(len(inputJSON)), ResponseBytes: uint64(len(outputJSON)),
			DurationNS: uint64(duration.Nanoseconds())})
		return result, output, err
	}
	return operationSpec{
		name: name, toolbox: toolbox, description: description, scope: scope,
		sideEffect: sideEffect, approval: approval, inputSchema: inputSchema, outputSchema: outputSchema,
		register: func(server *mcp.Server) {
			mcp.AddTool(server, tool, func(ctx context.Context, request *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error) {
				started := time.Now()
				result, output, err := observed(ctx, request, input)
				if err != nil || result != nil {
					return result, output, err
				}
				encoded, _ := json.Marshal(output)
				fallback := compactToolFallback(name, any(input), any(output))
				textBytes := len(fallback)
				if fallback == "" {
					// The MCP SDK serializes the structured result into text for
					// text-only clients when no explicit fallback is supplied.
					textBytes = len(encoded)
				}
				result = &mcp.CallToolResult{Meta: mcp.Meta{"repoplane/usage.v1": map[string]any{
					"structured_bytes": len(encoded), "text_bytes": textBytes,
					"server_duration_ms": time.Since(started).Seconds() * 1000,
				}}}
				if fallback != "" {
					result.Content = []mcp.Content{&mcp.TextContent{Text: fallback}}
				}
				return result, output, nil
			})
		},
		invoke: func(ctx context.Context, request *mcp.CallToolRequest, raw json.RawMessage) (*mcp.CallToolResult, any, error) {
			input, err := decodeStrict[In](raw)
			if err != nil {
				return nil, nil, publicError(err)
			}
			result, output, err := observed(ctx, request, input)
			return result, output, err
		},
		setObserver: func(callback func(context.Context, store.UsageEvent)) { observe = callback },
	}
}

func compactToolFallback(name string, input, output any) string {
	switch name {
	case ToolProjectRecords:
		request := input.(records.QueryRequest)
		response := output.(records.QueryResponse)
		if request.Mode == "get" || (request.Mode == "list" && request.ResponseView != "brief") ||
			((request.Mode == "get_topic" || (request.Mode == "" && request.TopicKey != "" && request.Query == "")) && response.Counts.Returned == 1) ||
			request.ResponseView == "full" || request.ResponseView == "discovery" || len(request.PayloadFields) > 0 {
			return ""
		}
		var lines []string
		for _, item := range response.Items {
			line := item.ID
			if item.Title != "" {
				line += " " + item.Title
			}
			if item.Summary != "" {
				line += ": " + item.Summary
			}
			if item.NextAction != "" {
				line += "; next: " + item.NextAction
			}
			if request.Mode == "resume" && response.Counts.Returned == 1 {
				if item.ChangeSummary != "" {
					line += "; changed: " + item.ChangeSummary
				}
				if len(item.EvidenceRefs) > 0 {
					line += "; evidence: " + strings.Join(item.EvidenceRefs, ", ")
				}
				if len(item.BackgroundRefs) > 0 {
					line += "; background: " + strings.Join(item.BackgroundRefs, ", ")
				}
			}
			lines = append(lines, line)
		}
		if response.NextCursor != nil {
			lines = append(lines, "next_cursor: "+*response.NextCursor)
		}
		for _, warning := range response.Warnings {
			lines = append(lines, "warning: "+warning.Code+" "+warning.Message)
		}
		if len(lines) == 0 {
			return "no records"
		}
		return strings.Join(lines, "\n")
	case ToolCheckpointWrite, ToolMemoWrite, ToolCheckReportImport:
		view := ""
		switch request := input.(type) {
		case records.CheckpointRequest:
			view = request.ResponseView
		case records.MemoRequest:
			view = request.ResponseView
		case records.ImportRequest:
			view = request.ResponseView
		}
		if view == "full" {
			return ""
		}
		response := output.(records.MutationResponse)
		line := fmt.Sprintf("%s id=%s revision=%d", response.Status, response.Record.ID, response.Record.Revision)
		for _, warning := range response.Warnings {
			line += "; warning: " + warning.Code + " " + warning.Message
		}
		return line
	}
	return ""
}

func decodeStrict[T any](raw json.RawMessage) (T, error) {
	var output T
	if len(raw) == 0 || len(raw) > maxToolboxArguments {
		return output, contracts.ErrLimitExceeded
	}
	if err := validateJSONBounds(raw); err != nil {
		return output, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&output); err != nil {
		return output, fmt.Errorf("invalid arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return output, errors.New("invalid arguments: multiple JSON values")
	}
	return output, nil
}

func validateJSONBounds(raw []byte) error {
	const maxDepth = 64
	const maxStructuralItems = 10_000
	depth, items := 0, 0
	inString, escaped := false, false
	for _, value := range raw {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if value == '\\' {
				escaped = true
			} else if value == '"' {
				inString = false
			}
			continue
		}
		switch value {
		case '"':
			inString = true
		case '{', '[':
			depth++
			items++
			if depth > maxDepth || items > maxStructuralItems {
				return contracts.ErrLimitExceeded
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return errors.New("invalid arguments structure")
			}
		case ',':
			items++
			if items > maxStructuralItems {
				return contracts.ErrLimitExceeded
			}
		}
	}
	if depth != 0 || inString {
		return errors.New("invalid arguments structure")
	}
	return nil
}

func operationRegistry(options Options) []operationSpec {
	operations := make([]operationSpec, 0, 14)
	if options.Catalog != nil {
		operations = append(operations, newOperation(ToolCatalogQuery, ToolboxRead, "Query the workspace catalog by search, get, list, audit, or status.", ScopeRead, "read_only", "none", nil,
			func(ctx context.Context, _ *mcp.CallToolRequest, input catalog.QueryRequest) (*mcp.CallToolResult, catalog.QueryResponse, error) {
				if err := authorize(ctx, options, ToolCatalogQuery); err != nil {
					return nil, catalog.QueryResponse{}, publicError(err)
				}
				output, err := options.Catalog.Query(ctx, input)
				return nil, output, publicError(err)
			}))
	}
	if options.Search != nil {
		operations = append(operations, newOperation(ToolWorkspaceSearch, ToolboxRead, "Search names, text, Git history, or configured symbols with bounded evidence; requests session read approval for an external root.", ScopeRead, "read_only", "conditional", nil,
			func(ctx context.Context, request *mcp.CallToolRequest, input search.Request) (*mcp.CallToolResult, search.Response, error) {
				if err := authorize(ctx, options, ToolWorkspaceSearch); err != nil {
					return nil, search.Response{}, publicError(err)
				}
				if result, ok, err := resumeApproval(request, options.RuntimeAccess); err != nil || !ok {
					return result, search.Response{}, publicError(err)
				}
				output, err := options.Search.Query(ctx, input)
				if errors.Is(err, workspace.ErrEscape) {
					return requestReadApproval[search.Response](options.RuntimeAccess, input.Root, ToolWorkspaceSearch)
				}
				return nil, output, publicError(err)
			}))
	}
	if options.PathFacts != nil {
		operations = append(operations, newOperation(ToolPathExplain, ToolboxRead, "Explain workspace, Git, link, encoding, newline, rule, and basename facts; requests session read approval for an external path.", ScopeRead, "read_only", "conditional", nil,
			func(ctx context.Context, request *mcp.CallToolRequest, input pathfacts.Request) (*mcp.CallToolResult, pathfacts.Response, error) {
				if err := authorize(ctx, options, ToolPathExplain); err != nil {
					return nil, pathfacts.Response{}, publicError(err)
				}
				if result, ok, err := resumeApproval(request, options.RuntimeAccess); err != nil || !ok {
					return result, pathfacts.Response{}, publicError(err)
				}
				output, err := options.PathFacts.Explain(ctx, input)
				if errors.Is(err, workspace.ErrEscape) {
					return requestReadApproval[pathfacts.Response](options.RuntimeAccess, input.Path, ToolPathExplain)
				}
				return nil, output, publicError(err)
			}))
	}
	if options.DataQuery != nil {
		operations = append(operations, newOperation(ToolDataQuery, ToolboxRead, "Read ranges or query JSON, JSONL, logs, CSV, and TSV; requests session read approval for an external source.", ScopeRead, "read_only", "conditional", nil,
			func(ctx context.Context, request *mcp.CallToolRequest, input dataquery.Request) (*mcp.CallToolResult, dataquery.Response, error) {
				if err := authorize(ctx, options, ToolDataQuery); err != nil {
					return nil, dataquery.Response{}, publicError(err)
				}
				if result, ok, err := resumeApproval(request, options.RuntimeAccess); err != nil || !ok {
					return result, dataquery.Response{}, publicError(err)
				}
				output, err := options.DataQuery.Query(ctx, input)
				if errors.Is(err, workspace.ErrEscape) {
					return requestReadApproval[dataquery.Response](options.RuntimeAccess, sourcePath(input.Ref), ToolDataQuery)
				}
				return nil, output, publicError(err)
			}))
	}
	if options.Records != nil {
		operations = append(operations, newOperation(ToolProjectRecords, ToolboxRead, "Search records as brief cards; get by ID, get_topic by exact current memo key, or resume a checkpoint with next steps and evidence.", ScopeRead, "read_only", "none", nil,
			func(ctx context.Context, _ *mcp.CallToolRequest, input records.QueryRequest) (*mcp.CallToolResult, records.QueryResponse, error) {
				if err := authorize(ctx, options, ToolProjectRecords); err != nil {
					return nil, records.QueryResponse{}, publicError(err)
				}
				output, err := options.Records.Query(ctx, input)
				return nil, output, publicError(err)
			}))
	}
	if options.CheckpointWriter != nil {
		operations = append(operations, newOperation(ToolCheckpointWrite, ToolboxWrite, "Write a task handoff; include change_summary and background_refs for useful one-call resume. May require approval.", ScopeIntentWrite, "write", "conditional", nil,
			func(ctx context.Context, request *mcp.CallToolRequest, input records.CheckpointRequest) (*mcp.CallToolResult, records.MutationResponse, error) {
				if err := authorize(ctx, options, ToolCheckpointWrite); err != nil {
					return nil, records.MutationResponse{}, publicMutationError(err)
				}
				if result, ok, err := ensureCapability(request, options.RuntimeAccess, runtimeaccess.KindIntentWrite, ToolCheckpointWrite); err != nil || !ok {
					return result, records.MutationResponse{}, publicMutationError(err)
				}
				output, err := options.CheckpointWriter.WriteCheckpoint(ctx, input)
				return nil, output, publicMutationError(err)
			}))
	}
	if options.MemoWriter != nil {
		operations = append(operations, newOperation(ToolMemoWrite, ToolboxWrite, "Write a memo or typed host fact. For reusable decisions include topic_key, title, summary, temporal_kind/as_of, and invalidation_condition. May require approval.", ScopeIntentWrite, "write", "conditional", nil,
			func(ctx context.Context, request *mcp.CallToolRequest, input records.MemoRequest) (*mcp.CallToolResult, records.MutationResponse, error) {
				if err := authorize(ctx, options, ToolMemoWrite); err != nil {
					return nil, records.MutationResponse{}, publicMutationError(err)
				}
				if result, ok, err := ensureCapability(request, options.RuntimeAccess, runtimeaccess.KindIntentWrite, ToolMemoWrite); err != nil || !ok {
					return result, records.MutationResponse{}, publicMutationError(err)
				}
				output, err := options.MemoWriter.WriteMemo(ctx, input)
				return nil, output, publicMutationError(err)
			}))
	}
	if options.ReportImporter != nil {
		operations = append(operations, newOperation(ToolCheckReportImport, ToolboxImport, "Import a local check report; may require approval.", ScopeReportImport, "write", "conditional", nil,
			func(ctx context.Context, request *mcp.CallToolRequest, input records.ImportRequest) (*mcp.CallToolResult, records.MutationResponse, error) {
				if err := authorize(ctx, options, ToolCheckReportImport); err != nil {
					return nil, records.MutationResponse{}, publicMutationError(err)
				}
				if result, ok, err := ensureCapability(request, options.RuntimeAccess, runtimeaccess.KindReportImport, ToolCheckReportImport); err != nil || !ok {
					return result, records.MutationResponse{}, publicMutationError(err)
				}
				output, err := options.ReportImporter.ImportReport(ctx, input)
				return nil, output, publicMutationError(err)
			}))
	}
	if options.Runner != nil {
		nonDestructive, destructive := false, true
		operations = append(operations,
			newOperation(ToolRunPrepare, ToolboxRunner, "Prepare a registered run; return a concise plan and run ID.", ScopeRunnerExecute, "plan_write", "conditional", &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, IdempotentHint: false},
				func(ctx context.Context, request *mcp.CallToolRequest, input runner.PrepareRequest) (*mcp.CallToolResult, runner.PrepareResponse, error) {
					if err := authorize(ctx, options, ToolRunPrepare); err != nil {
						return nil, runner.PrepareResponse{}, publicError(err)
					}
					if result, ok, err := ensureCapability(request, options.RuntimeAccess, runtimeaccess.KindRunner, ToolRunPrepare); err != nil || !ok {
						return result, runner.PrepareResponse{}, publicError(err)
					}
					output, err := options.Runner.Prepare(ctx, input)
					return nil, output, publicError(err)
				}),
			newOperation(ToolRunExecute, ToolboxRunner, "Run/reuse a prepared plan; may request approval.", ScopeRunnerExecute, "execute", "conditional", &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false},
				func(ctx context.Context, request *mcp.CallToolRequest, input runner.ExecuteRequest) (*mcp.CallToolResult, runner.ExecuteResponse, error) {
					if err := authorize(ctx, options, ToolRunExecute); err != nil {
						return nil, runner.ExecuteResponse{}, publicError(err)
					}
					if result, ok, err := ensureCapability(request, options.RuntimeAccess, runtimeaccess.KindRunner, ToolRunExecute); err != nil || !ok {
						return result, runner.ExecuteResponse{}, publicError(err)
					}
					output, err := options.Runner.Execute(ctx, input)
					return nil, output, publicError(err)
				}),
			newOperation(ToolRunInspect, ToolboxRunner, "Refs default. Local state: file_ref is under runtime_config(status).configuration.state_dir[0]; HTTP use response_view=bytes. Can cancel.", ScopeRunnerExecute, "inspect_or_cancel", "conditional", &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false},
				func(ctx context.Context, request *mcp.CallToolRequest, input runner.InspectRequest) (*mcp.CallToolResult, runner.InspectResponse, error) {
					if err := authorize(ctx, options, ToolRunInspect); err != nil {
						return nil, runner.InspectResponse{}, publicError(err)
					}
					if result, ok, err := ensureCapability(request, options.RuntimeAccess, runtimeaccess.KindRunner, ToolRunInspect); err != nil || !ok {
						return result, runner.InspectResponse{}, publicError(err)
					}
					output, err := options.Runner.Inspect(ctx, input)
					return nil, output, publicError(err)
				}),
		)
	}
	if options.MemoryBackup != nil {
		nonDestructive := false
		operations = append(operations, newOperation(ToolMemoryBackup, ToolboxState, "Export portable durable memory and retained Runner evidence without secrets or restart; requests approval for the destination directory.", ScopeStateExport, "state_export", "conditional", &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, IdempotentHint: false},
			func(ctx context.Context, request *mcp.CallToolRequest, input memorybackup.Request) (*mcp.CallToolResult, memorybackup.Response, error) {
				if err := authorize(ctx, options, ToolMemoryBackup); err != nil {
					return nil, memorybackup.Response{}, publicError(err)
				}
				if result, ok, err := ensureExportDestination(request, options.RuntimeAccess, input.Destination, ToolMemoryBackup); err != nil || !ok {
					return result, memorybackup.Response{}, publicError(err)
				}
				if options.RuntimeAccess != nil {
					approved, ok := options.RuntimeAccess.ApprovedPath(runtimeaccess.KindMemoryExport, input.Destination)
					if !ok {
						return nil, memorybackup.Response{}, publicError(runtimeaccess.ErrPending)
					}
					input.Destination = approved
				}
				output, err := options.MemoryBackup.Export(ctx, input)
				return nil, output, publicError(err)
			}))
	}
	if options.RuntimeAccess != nil {
		destructive := true
		operations = append(operations, newOperation(ToolRuntimeAccess, ToolboxRead, "Inspect, grant with explicit user approval, or revoke ephemeral stdio access without configuration edits or restart.", ScopeRead, "runtime_access_change", "explicit", &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false},
			func(ctx context.Context, request *mcp.CallToolRequest, input runtimeaccess.Request) (*mcp.CallToolResult, runtimeaccess.Response, error) {
				if err := authorize(ctx, options, ToolRuntimeAccess); err != nil {
					return nil, runtimeaccess.Response{}, publicError(err)
				}
				switch input.Action {
				case runtimeaccess.ActionStatus:
					return nil, options.RuntimeAccess.Status(), nil
				case runtimeaccess.ActionRevoke:
					output, err := options.RuntimeAccess.Revoke(input.GrantID)
					return nil, output, publicError(err)
				case runtimeaccess.ActionGrant:
					if request.Params.RequestState == "" {
						token, message, err := options.RuntimeAccess.Begin(input.Kind, input.Path)
						if err != nil {
							return nil, runtimeaccess.Response{}, publicError(err)
						}
						return approvalResult(token, approvalMessage(ToolRuntimeAccess, message)), runtimeaccess.Response{}, nil
					}
					output, err := completeApproval(request, options.RuntimeAccess)
					return nil, output, publicError(err)
				default:
					return nil, runtimeaccess.Response{}, publicError(runtimeaccess.ErrInvalid)
				}
			}))
	}
	if options.RuntimeConfig != nil && options.RuntimeAccess != nil {
		destructive := true
		operations = append(operations, newOperation(ToolRuntimeConfig, ToolboxState, "Inspect or change live sources, workspace, state, or HTTP without restarting stdio.", ScopeStateExport, "runtime_configuration_change", "explicit", &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false},
			func(ctx context.Context, request *mcp.CallToolRequest, input runtimeconfig.Request) (*mcp.CallToolResult, runtimeconfig.Response, error) {
				if err := authorize(ctx, options, ToolRuntimeConfig); err != nil {
					return nil, runtimeconfig.Response{}, publicError(err)
				}
				if !options.RuntimeAccess.Available() {
					return nil, runtimeconfig.Response{}, publicError(runtimeconfig.ErrUnavailable)
				}
				if input.Action == runtimeconfig.ActionStatus {
					output, err := options.RuntimeConfig.Status(ctx)
					return nil, output, publicError(err)
				}
				if request.Params.RequestState == "" {
					token, message, err := options.RuntimeAccess.BeginConfig(input)
					if err != nil {
						return nil, runtimeconfig.Response{}, publicError(err)
					}
					return approvalResult(token, approvalMessage(ToolRuntimeConfig, message)), runtimeconfig.Response{}, nil
				}
				accepted, err := approvalAccepted(request)
				if err != nil {
					return nil, runtimeconfig.Response{}, publicError(err)
				}
				output, err := options.RuntimeAccess.CompleteConfig(ctx, request.Params.RequestState, accepted, options.RuntimeConfig)
				return nil, output, publicError(err)
			}))
	}
	for i := range operations {
		operations[i].setObserver(options.ObserveUsage)
	}
	return operations
}

func registerToolboxes(server *mcp.Server, options Options, operations []operationSpec) {
	byToolbox := make(map[string][]operationSpec)
	for _, operation := range operations {
		byToolbox[operation.toolbox] = append(byToolbox[operation.toolbox], operation)
	}
	descriptions := map[string]string{
		ToolboxRead:   "Describe or call bounded repository read and runtime-access operations.",
		ToolboxWrite:  "Describe or call checkpoint and memo write operations.",
		ToolboxImport: "Describe or call verification report import operations.",
		ToolboxRunner: "Describe or call registered Runner operations.",
		ToolboxState:  "Describe or call runtime configuration and memory export operations.",
	}
	destructive := true
	for _, toolbox := range ToolboxNames() {
		group := byToolbox[toolbox]
		if len(group) == 0 {
			continue
		}
		allowed := make(map[string]operationSpec, len(group))
		names := make([]string, 0, len(group))
		for _, operation := range group {
			allowed[operation.name] = operation
			names = append(names, operation.name)
		}
		sort.Strings(names)
		inputSchema := toolboxInputSchema(names)
		toolboxName := toolbox
		mcp.AddTool(server, &mcp.Tool{
			Name: toolboxName, Description: descriptions[toolboxName], InputSchema: inputSchema,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false},
		}, func(ctx context.Context, request *mcp.CallToolRequest, input toolboxRequest) (*mcp.CallToolResult, toolboxResponse, error) {
			return handleToolbox(ctx, request, options, toolboxName, allowed, input)
		})
	}
}

func toolboxInputSchema(operations []string) *jsonschema.Schema {
	values := make([]any, len(operations))
	for index, operation := range operations {
		values[index] = operation
	}
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"action":              {Type: "string", Enum: []any{"describe", "call"}, Description: "describe an operation contract or call it"},
			"operation":           {Type: "string", Enum: values, Description: "operation in this toolbox"},
			"detail":              {Type: "string", Enum: []any{"compact", "complete"}, Description: "describe detail; default compact"},
			"known_schema_handle": {Type: "string", Description: "current handle already available to the caller; describe only"},
			"schema_handle":       {Type: "string", Description: "exact handle returned by describe; call only"},
			"arguments":           {Type: "object", Description: "operation arguments validated against the described contract"},
		},
		Required:             []string{"action", "operation"},
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
	}
}

func handleToolbox(ctx context.Context, request *mcp.CallToolRequest, options Options, toolbox string, allowed map[string]operationSpec, input toolboxRequest) (*mcp.CallToolResult, toolboxResponse, error) {
	var zero toolboxResponse
	if len(input.Action)+len(input.Operation)+len(input.Detail)+len(input.KnownSchemaHandle)+len(input.SchemaHandle) > 8192 {
		return nil, zero, publicError(contracts.ErrLimitExceeded)
	}
	if err := authorize(ctx, options, toolbox); err != nil {
		return nil, zero, publicError(err)
	}
	operation, ok := allowed[input.Operation]
	if !ok {
		return nil, zero, publicError(errors.New("invalid toolbox operation"))
	}
	if err := authorize(ctx, options, operation.name); err != nil {
		return nil, zero, publicError(err)
	}
	if options.AuditOperation != nil {
		if err := options.AuditOperation(ctx, toolbox, operation.name); err != nil {
			return nil, zero, publicError(err)
		}
	}
	handle, compact, complete, err := contractsFor(operation)
	if err != nil {
		return nil, zero, publicError(err)
	}
	switch input.Action {
	case "describe":
		if input.SchemaHandle != "" || len(input.Arguments) != 0 {
			return nil, zero, publicError(errors.New("invalid describe fields"))
		}
		if input.Detail != "" && input.Detail != "compact" && input.Detail != "complete" {
			return nil, zero, publicError(errors.New("invalid describe detail"))
		}
		if input.KnownSchemaHandle == handle {
			return nil, toolboxResponse{Status: "unchanged", Operation: operation.name, SchemaHandle: handle}, nil
		}
		contract := compact
		if input.Detail == "complete" {
			contract = complete
		}
		return nil, toolboxResponse{Status: "ok", Operation: operation.name, SchemaHandle: handle, Contract: &contract}, nil
	case "call":
		if input.Detail != "" || input.KnownSchemaHandle != "" || input.SchemaHandle == "" || len(input.Arguments) == 0 {
			return nil, zero, publicError(errors.New("invalid call fields"))
		}
		if input.SchemaHandle != handle {
			return nil, toolboxResponse{Status: "schema_changed", Operation: operation.name, SchemaHandle: handle, Contract: &compact}, nil
		}
		result, output, err := operation.invoke(ctx, request, input.Arguments)
		if err != nil || result != nil {
			return result, zero, err
		}
		return nil, toolboxResponse{Status: "ok", Operation: operation.name, SchemaHandle: handle, Result: output}, nil
	default:
		return nil, zero, publicError(errors.New("invalid toolbox action"))
	}
}

func contractsFor(operation operationSpec) (string, operationContract, operationContract, error) {
	complete := operationContract{
		SchemaVersion: toolboxContractVersion,
		Description:   operation.description,
		InputSchema:   operation.inputSchema,
		OutputSchema:  operation.outputSchema,
		ResultSummary: "typed operation result; request detail=complete to include its output schema",
		Scope:         operation.scope,
		SideEffect:    operation.sideEffect,
		Approval:      operation.approval,
		Retry:         "re-describe after schema_changed; never retry a side effect without inspecting its result",
	}
	canonical, err := json.Marshal(struct {
		Toolbox   string            `json:"toolbox"`
		Operation string            `json:"operation"`
		Contract  operationContract `json:"contract"`
	}{operation.toolbox, operation.name, complete})
	if err != nil {
		return "", operationContract{}, operationContract{}, err
	}
	digest := sha256.Sum256(canonical)
	handle := "schema:" + operation.name + "@sha256:" + hex.EncodeToString(digest[:])
	compact := complete
	compact.OutputSchema = nil
	return handle, compact, complete, nil
}
