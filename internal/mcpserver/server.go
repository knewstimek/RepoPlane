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
			Name:        "catalog_query",
			Description: "Search, inspect, enumerate, audit, or report status for the workspace tool catalog.",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input catalog.QueryRequest) (*mcp.CallToolResult, catalog.QueryResponse, error) {
			output, err := options.Catalog.Query(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.Search != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "workspace_search",
			Description: "Search workspace filenames or UTF-8, CP949, or EUC-KR text with explicit scope, coverage, and bounded results.",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input search.Request) (*mcp.CallToolResult, search.Response, error) {
			output, err := options.Search.Query(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.PathFacts != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "path_explain",
			Description: "Explain observable workspace, Git, link, encoding, newline, rule-scope, and basename facts for a path.",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input pathfacts.Request) (*mcp.CallToolResult, pathfacts.Response, error) {
			output, err := options.PathFacts.Explain(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.DataQuery != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "data_query",
			Description: "Read bounded text line/byte ranges or filter and project JSONL records without losing integer precision.",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input dataquery.Request) (*mcp.CallToolResult, dataquery.Response, error) {
			output, err := options.DataQuery.Query(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.Records != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "project_records", Description: "Search and read durable verification, checkpoint, memo, environment, run, and artifact records without modifying them."}, func(ctx context.Context, _ *mcp.CallToolRequest, input records.QueryRequest) (*mcp.CallToolResult, records.QueryResponse, error) {
			output, err := options.Records.Query(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.CheckpointWriter != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "checkpoint_write", Description: "Create, update, or supersede a bounded task checkpoint using optimistic concurrency."}, func(ctx context.Context, _ *mcp.CallToolRequest, input records.CheckpointRequest) (*mcp.CallToolResult, records.MutationResponse, error) {
			output, err := options.CheckpointWriter.WriteCheckpoint(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.MemoWriter != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "memo_write", Description: "Create, update, or supersede a bounded decision or failure memo using optimistic concurrency."}, func(ctx context.Context, _ *mcp.CallToolRequest, input records.MemoRequest) (*mcp.CallToolResult, records.MutationResponse, error) {
			output, err := options.MemoWriter.WriteMemo(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.ReportImporter != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "check_report_import", Description: "Import a bounded workspace-local check-report.v1 without storing its raw contents."}, func(ctx context.Context, _ *mcp.CallToolRequest, input records.ImportRequest) (*mcp.CallToolResult, records.MutationResponse, error) {
			output, err := options.ReportImporter.ImportReport(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.Runner != nil {
		nonDestructive := false
		destructive := true
		mcp.AddTool(server, &mcp.Tool{Name: "run_prepare", Description: "Validate a registered capability, observe its required environment, and create a durable execution plan.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, IdempotentHint: false}}, func(ctx context.Context, _ *mcp.CallToolRequest, input runner.PrepareRequest) (*mcp.CallToolResult, runner.PrepareResponse, error) {
			output, err := options.Runner.Prepare(ctx, input)
			return nil, output, publicError(err)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "run_execute", Description: "Execute one prepared registered-capability plan after revalidating relevant inputs and identities.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false}}, func(ctx context.Context, _ *mcp.CallToolRequest, input runner.ExecuteRequest) (*mcp.CallToolResult, runner.ExecuteResponse, error) {
			output, err := options.Runner.Execute(ctx, input)
			return nil, output, publicError(err)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "run_inspect", Description: "Inspect a run, page bounded stdout, stderr, or retained artifacts, or request cancellation.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false}}, func(ctx context.Context, _ *mcp.CallToolRequest, input runner.InspectRequest) (*mcp.CallToolResult, runner.InspectResponse, error) {
			output, err := options.Runner.Inspect(ctx, input)
			return nil, output, publicError(err)
		})
	}
	return server
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
