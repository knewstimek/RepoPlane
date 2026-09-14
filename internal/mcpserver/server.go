// Package mcpserver adapts RepoPlane services to the Model Context Protocol.
package mcpserver

import (
	"context"
	"errors"
	"io/fs"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"repoplane/internal/catalog"
	"repoplane/internal/contracts"
	"repoplane/internal/cursor"
	"repoplane/internal/dataquery"
	"repoplane/internal/pathfacts"
	"repoplane/internal/records"
	"repoplane/internal/runner"
	"repoplane/internal/search"
	"repoplane/internal/store"
	"repoplane/internal/textcodec"
	"repoplane/internal/workspace"
)

const serverName = "repoplane"

var ErrAuthorizationDenied = errors.New("mcpserver: authorization denied")

// New constructs a RepoPlane MCP server. Tool registration is added by the
// feature milestones; constructing the server itself has no workspace side
// effects.
type Options struct {
	Catalog          *catalog.Service
	Search           *search.Service
	PathFacts        *pathfacts.Service
	DataQuery        *dataquery.Service
	Records          *records.Service
	CheckpointWriter *records.Service
	MemoWriter       *records.Service
	ReportImporter   *records.Service
	Runner           *runner.Service
	Authorize        func(context.Context, string) error
}

func New(version string, provided ...Options) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: version,
	}, nil)
	var options Options
	if len(provided) > 0 {
		options = provided[0]
	}
	if options.Catalog != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name:        ToolCatalogQuery,
			Description: "Query the workspace catalog by search, get, list, audit, or status.",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input catalog.QueryRequest) (*mcp.CallToolResult, catalog.QueryResponse, error) {
			if err := authorize(ctx, options, ToolCatalogQuery); err != nil {
				return nil, catalog.QueryResponse{}, publicError(err)
			}
			output, err := options.Catalog.Query(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.Search != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name:        ToolWorkspaceSearch,
			Description: "Search names, text, Git history, or configured symbols with bounded evidence.",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input search.Request) (*mcp.CallToolResult, search.Response, error) {
			if err := authorize(ctx, options, ToolWorkspaceSearch); err != nil {
				return nil, search.Response{}, publicError(err)
			}
			output, err := options.Search.Query(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.PathFacts != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name:        ToolPathExplain,
			Description: "Explain workspace, Git, link, encoding, newline, rule, and basename facts for a path.",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input pathfacts.Request) (*mcp.CallToolResult, pathfacts.Response, error) {
			if err := authorize(ctx, options, ToolPathExplain); err != nil {
				return nil, pathfacts.Response{}, publicError(err)
			}
			output, err := options.PathFacts.Explain(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.DataQuery != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name:        ToolDataQuery,
			Description: "Read ranges or query JSON, JSONL, logs, CSV, and TSV with bounded precision.",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input dataquery.Request) (*mcp.CallToolResult, dataquery.Response, error) {
			if err := authorize(ctx, options, ToolDataQuery); err != nil {
				return nil, dataquery.Response{}, publicError(err)
			}
			output, err := options.DataQuery.Query(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.Records != nil {
		mcp.AddTool(server, &mcp.Tool{Name: ToolProjectRecords, Description: "Query durable verification, checkpoint, memo, environment, run, and artifact records."}, func(ctx context.Context, _ *mcp.CallToolRequest, input records.QueryRequest) (*mcp.CallToolResult, records.QueryResponse, error) {
			if err := authorize(ctx, options, ToolProjectRecords); err != nil {
				return nil, records.QueryResponse{}, publicError(err)
			}
			output, err := options.Records.Query(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.CheckpointWriter != nil {
		mcp.AddTool(server, &mcp.Tool{Name: ToolCheckpointWrite, Description: "Create, update, or supersede a bounded checkpoint with optimistic concurrency."}, func(ctx context.Context, _ *mcp.CallToolRequest, input records.CheckpointRequest) (*mcp.CallToolResult, records.MutationResponse, error) {
			if err := authorize(ctx, options, ToolCheckpointWrite); err != nil {
				return nil, records.MutationResponse{}, publicError(err)
			}
			output, err := options.CheckpointWriter.WriteCheckpoint(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.MemoWriter != nil {
		mcp.AddTool(server, &mcp.Tool{Name: ToolMemoWrite, Description: "Create, update, or supersede a bounded memo with optimistic concurrency."}, func(ctx context.Context, _ *mcp.CallToolRequest, input records.MemoRequest) (*mcp.CallToolResult, records.MutationResponse, error) {
			if err := authorize(ctx, options, ToolMemoWrite); err != nil {
				return nil, records.MutationResponse{}, publicError(err)
			}
			output, err := options.MemoWriter.WriteMemo(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.ReportImporter != nil {
		mcp.AddTool(server, &mcp.Tool{Name: ToolCheckReportImport, Description: "Import a bounded local check-report.v1 without storing raw contents."}, func(ctx context.Context, _ *mcp.CallToolRequest, input records.ImportRequest) (*mcp.CallToolResult, records.MutationResponse, error) {
			if err := authorize(ctx, options, ToolCheckReportImport); err != nil {
				return nil, records.MutationResponse{}, publicError(err)
			}
			output, err := options.ReportImporter.ImportReport(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.Runner != nil {
		nonDestructive := false
		destructive := true
		mcp.AddTool(server, &mcp.Tool{Name: ToolRunPrepare, Description: "Validate a capability and create a durable environment-bound plan.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, IdempotentHint: false}}, func(ctx context.Context, _ *mcp.CallToolRequest, input runner.PrepareRequest) (*mcp.CallToolResult, runner.PrepareResponse, error) {
			if err := authorize(ctx, options, ToolRunPrepare); err != nil {
				return nil, runner.PrepareResponse{}, publicError(err)
			}
			output, err := options.Runner.Prepare(ctx, input)
			return nil, output, publicError(err)
		})
		mcp.AddTool(server, &mcp.Tool{Name: ToolRunExecute, Description: "Execute or reuse one plan after dependency revalidation.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false}}, func(ctx context.Context, _ *mcp.CallToolRequest, input runner.ExecuteRequest) (*mcp.CallToolResult, runner.ExecuteResponse, error) {
			if err := authorize(ctx, options, ToolRunExecute); err != nil {
				return nil, runner.ExecuteResponse{}, publicError(err)
			}
			output, err := options.Runner.Execute(ctx, input)
			return nil, output, publicError(err)
		})
		mcp.AddTool(server, &mcp.Tool{Name: ToolRunInspect, Description: "Inspect/cancel a run or page retained streams and artifacts.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false}}, func(ctx context.Context, _ *mcp.CallToolRequest, input runner.InspectRequest) (*mcp.CallToolResult, runner.InspectResponse, error) {
			if err := authorize(ctx, options, ToolRunInspect); err != nil {
				return nil, runner.InspectResponse{}, publicError(err)
			}
			output, err := options.Runner.Inspect(ctx, input)
			return nil, output, publicError(err)
		})
	}
	return server
}

func authorize(ctx context.Context, options Options, tool string) error {
	if options.Authorize == nil {
		return nil
	}
	return options.Authorize(ctx, tool)
}

// publicError exposes only stable machine codes to MCP clients. In particular,
// it never forwards database diagnostics or absolute host paths.
func publicError(err error) error {
	if err == nil {
		return nil
	}
	code := "internal_error"
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		code = "deadline_exceeded"
	case errors.Is(err, workspace.ErrEscape), errors.Is(err, workspace.ErrAbsolute):
		code = "workspace_escape"
	case errors.Is(err, cursor.ErrExpired):
		code = "cursor_expired"
	case errors.Is(err, cursor.ErrInvalid):
		code = "cursor_invalid"
	case errors.Is(err, dataquery.ErrSourceChanged):
		code = "source_changed"
	case errors.Is(err, fs.ErrNotExist):
		code = "source_not_found"
	case errors.Is(err, store.ErrNotFound):
		code = "record_not_found"
	case errors.Is(err, store.ErrConflict):
		code = "revision_conflict"
	case errors.Is(err, records.ErrPermissionDenied):
		code = "permission_denied"
	case errors.Is(err, ErrAuthorizationDenied):
		code = "permission_denied"
	case errors.Is(err, records.ErrReportInvalid):
		code = "report_invalid"
	case errors.Is(err, runner.ErrNotExecutable):
		code = "capability_not_executable"
	case errors.Is(err, runner.ErrPlanStale):
		code = "plan_stale"
	case errors.Is(err, runner.ErrRunState):
		code = "run_state_conflict"
	case errors.Is(err, contracts.ErrLimitExceeded),
		errors.Is(err, catalog.ErrResponseTooLarge), errors.Is(err, search.ErrResponseTooLarge),
		errors.Is(err, pathfacts.ErrResponseTooLarge), errors.Is(err, dataquery.ErrResponseTooLarge), errors.Is(err, records.ErrResponseTooLarge),
		errors.Is(err, dataquery.ErrSourceTooLarge):
		code = "limit_exceeded"
	case errors.Is(err, search.ErrBackendUnavailable):
		code = "backend_unavailable"
	case errors.Is(err, textcodec.ErrInvalid):
		code = "decode_failed"
	case errors.Is(err, textcodec.ErrUnsupported), strings.Contains(err.Error(), "unsupported"):
		code = "unsupported_operation"
	case strings.Contains(err.Error(), "required"), strings.Contains(err.Error(), "invalid"),
		strings.Contains(err.Error(), "must be"), strings.Contains(err.Error(), "provide exactly"),
		strings.Contains(err.Error(), "malformed"):
		code = "invalid_argument"
	}
	return errors.New(code)
}

// RunStdio serves MCP frames on stdin/stdout until the client disconnects or
// ctx is cancelled. Diagnostics must never be written to stdout.
func RunStdio(ctx context.Context, version string, options ...Options) error {
	return New(version, options...).Run(ctx, &mcp.StdioTransport{})
}
