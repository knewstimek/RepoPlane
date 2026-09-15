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
	"repoplane/internal/memorybackup"
	"repoplane/internal/pathfacts"
	"repoplane/internal/records"
	"repoplane/internal/runner"
	"repoplane/internal/runtimeaccess"
	"repoplane/internal/runtimeconfig"
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
	Catalog interface {
		Query(context.Context, catalog.QueryRequest) (catalog.QueryResponse, error)
	}
	Search interface {
		Query(context.Context, search.Request) (search.Response, error)
	}
	PathFacts interface {
		Explain(context.Context, pathfacts.Request) (pathfacts.Response, error)
	}
	DataQuery interface {
		Query(context.Context, dataquery.Request) (dataquery.Response, error)
	}
	Records interface {
		Query(context.Context, records.QueryRequest) (records.QueryResponse, error)
	}
	CheckpointWriter interface {
		WriteCheckpoint(context.Context, records.CheckpointRequest) (records.MutationResponse, error)
	}
	MemoWriter interface {
		WriteMemo(context.Context, records.MemoRequest) (records.MutationResponse, error)
	}
	ReportImporter interface {
		ImportReport(context.Context, records.ImportRequest) (records.MutationResponse, error)
	}
	Runner interface {
		Prepare(context.Context, runner.PrepareRequest) (runner.PrepareResponse, error)
		Execute(context.Context, runner.ExecuteRequest) (runner.ExecuteResponse, error)
		Inspect(context.Context, runner.InspectRequest) (runner.InspectResponse, error)
	}
	RuntimeAccess *runtimeaccess.Service
	RuntimeConfig runtimeconfig.Controller
	MemoryBackup  interface {
		Export(context.Context, memorybackup.Request) (memorybackup.Response, error)
	}
	Authorize func(context.Context, string) error
}

func New(version string, provided ...Options) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version}, nil)
	var options Options
	if len(provided) > 0 {
		options = provided[0]
	}
	if options.Catalog != nil {
		mcp.AddTool(server, &mcp.Tool{Name: ToolCatalogQuery, Description: "Query the workspace catalog by search, get, list, audit, or status."}, func(ctx context.Context, _ *mcp.CallToolRequest, input catalog.QueryRequest) (*mcp.CallToolResult, catalog.QueryResponse, error) {
			if err := authorize(ctx, options, ToolCatalogQuery); err != nil {
				return nil, catalog.QueryResponse{}, publicError(err)
			}
			output, err := options.Catalog.Query(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.Search != nil {
		mcp.AddTool(server, &mcp.Tool{Name: ToolWorkspaceSearch, Description: "Search names, text, Git history, or configured symbols with bounded evidence; requests session read approval for an external root."}, func(ctx context.Context, req *mcp.CallToolRequest, input search.Request) (*mcp.CallToolResult, search.Response, error) {
			if err := authorize(ctx, options, ToolWorkspaceSearch); err != nil {
				return nil, search.Response{}, publicError(err)
			}
			if result, ok, err := resumeApproval(req, options.RuntimeAccess); err != nil || !ok {
				return result, search.Response{}, publicError(err)
			}
			output, err := options.Search.Query(ctx, input)
			if errors.Is(err, workspace.ErrEscape) {
				return requestReadApproval[search.Response](options.RuntimeAccess, input.Root)
			}
			return nil, output, publicError(err)
		})
	}
	if options.PathFacts != nil {
		mcp.AddTool(server, &mcp.Tool{Name: ToolPathExplain, Description: "Explain workspace, Git, link, encoding, newline, rule, and basename facts; requests session read approval for an external path."}, func(ctx context.Context, req *mcp.CallToolRequest, input pathfacts.Request) (*mcp.CallToolResult, pathfacts.Response, error) {
			if err := authorize(ctx, options, ToolPathExplain); err != nil {
				return nil, pathfacts.Response{}, publicError(err)
			}
			if result, ok, err := resumeApproval(req, options.RuntimeAccess); err != nil || !ok {
				return result, pathfacts.Response{}, publicError(err)
			}
			output, err := options.PathFacts.Explain(ctx, input)
			if errors.Is(err, workspace.ErrEscape) {
				return requestReadApproval[pathfacts.Response](options.RuntimeAccess, input.Path)
			}
			return nil, output, publicError(err)
		})
	}
	if options.DataQuery != nil {
		mcp.AddTool(server, &mcp.Tool{Name: ToolDataQuery, Description: "Read ranges or query JSON, JSONL, logs, CSV, and TSV; requests session read approval for an external source."}, func(ctx context.Context, req *mcp.CallToolRequest, input dataquery.Request) (*mcp.CallToolResult, dataquery.Response, error) {
			if err := authorize(ctx, options, ToolDataQuery); err != nil {
				return nil, dataquery.Response{}, publicError(err)
			}
			if result, ok, err := resumeApproval(req, options.RuntimeAccess); err != nil || !ok {
				return result, dataquery.Response{}, publicError(err)
			}
			output, err := options.DataQuery.Query(ctx, input)
			if errors.Is(err, workspace.ErrEscape) {
				return requestReadApproval[dataquery.Response](options.RuntimeAccess, sourcePath(input.Ref))
			}
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
		mcp.AddTool(server, &mcp.Tool{Name: ToolCheckpointWrite, Description: "Create, update, or supersede a bounded checkpoint; requests session write approval when needed."}, func(ctx context.Context, req *mcp.CallToolRequest, input records.CheckpointRequest) (*mcp.CallToolResult, records.MutationResponse, error) {
			if err := authorize(ctx, options, ToolCheckpointWrite); err != nil {
				return nil, records.MutationResponse{}, publicError(err)
			}
			if result, ok, err := ensureCapability(req, options.RuntimeAccess, runtimeaccess.KindIntentWrite); err != nil || !ok {
				return result, records.MutationResponse{}, publicError(err)
			}
			output, err := options.CheckpointWriter.WriteCheckpoint(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.MemoWriter != nil {
		mcp.AddTool(server, &mcp.Tool{Name: ToolMemoWrite, Description: "Create, update, or supersede a bounded memo; requests session write approval when needed."}, func(ctx context.Context, req *mcp.CallToolRequest, input records.MemoRequest) (*mcp.CallToolResult, records.MutationResponse, error) {
			if err := authorize(ctx, options, ToolMemoWrite); err != nil {
				return nil, records.MutationResponse{}, publicError(err)
			}
			if result, ok, err := ensureCapability(req, options.RuntimeAccess, runtimeaccess.KindIntentWrite); err != nil || !ok {
				return result, records.MutationResponse{}, publicError(err)
			}
			output, err := options.MemoWriter.WriteMemo(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.ReportImporter != nil {
		mcp.AddTool(server, &mcp.Tool{Name: ToolCheckReportImport, Description: "Import a bounded local check report; requests session import approval when needed."}, func(ctx context.Context, req *mcp.CallToolRequest, input records.ImportRequest) (*mcp.CallToolResult, records.MutationResponse, error) {
			if err := authorize(ctx, options, ToolCheckReportImport); err != nil {
				return nil, records.MutationResponse{}, publicError(err)
			}
			if result, ok, err := ensureCapability(req, options.RuntimeAccess, runtimeaccess.KindReportImport); err != nil || !ok {
				return result, records.MutationResponse{}, publicError(err)
			}
			output, err := options.ReportImporter.ImportReport(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.Runner != nil {
		nonDestructive, destructive := false, true
		mcp.AddTool(server, &mcp.Tool{Name: ToolRunPrepare, Description: "Validate a capability and create a durable environment-bound plan; requests session execution approval when needed.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, IdempotentHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, input runner.PrepareRequest) (*mcp.CallToolResult, runner.PrepareResponse, error) {
			if err := authorize(ctx, options, ToolRunPrepare); err != nil {
				return nil, runner.PrepareResponse{}, publicError(err)
			}
			if result, ok, err := ensureCapability(req, options.RuntimeAccess, runtimeaccess.KindRunner); err != nil || !ok {
				return result, runner.PrepareResponse{}, publicError(err)
			}
			output, err := options.Runner.Prepare(ctx, input)
			return nil, output, publicError(err)
		})
		mcp.AddTool(server, &mcp.Tool{Name: ToolRunExecute, Description: "Execute or reuse one plan after dependency revalidation; requests session execution approval when needed.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, input runner.ExecuteRequest) (*mcp.CallToolResult, runner.ExecuteResponse, error) {
			if err := authorize(ctx, options, ToolRunExecute); err != nil {
				return nil, runner.ExecuteResponse{}, publicError(err)
			}
			if result, ok, err := ensureCapability(req, options.RuntimeAccess, runtimeaccess.KindRunner); err != nil || !ok {
				return result, runner.ExecuteResponse{}, publicError(err)
			}
			output, err := options.Runner.Execute(ctx, input)
			return nil, output, publicError(err)
		})
		mcp.AddTool(server, &mcp.Tool{Name: ToolRunInspect, Description: "Inspect/cancel a run or page retained streams and artifacts; requests session execution approval when needed.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, input runner.InspectRequest) (*mcp.CallToolResult, runner.InspectResponse, error) {
			if err := authorize(ctx, options, ToolRunInspect); err != nil {
				return nil, runner.InspectResponse{}, publicError(err)
			}
			if result, ok, err := ensureCapability(req, options.RuntimeAccess, runtimeaccess.KindRunner); err != nil || !ok {
				return result, runner.InspectResponse{}, publicError(err)
			}
			output, err := options.Runner.Inspect(ctx, input)
			return nil, output, publicError(err)
		})
	}
	if options.MemoryBackup != nil {
		destructive := false
		mcp.AddTool(server, &mcp.Tool{Name: ToolMemoryBackup, Description: "Export portable durable memory and retained Runner evidence without secrets or restart; requests approval for the destination directory.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, input memorybackup.Request) (*mcp.CallToolResult, memorybackup.Response, error) {
			if err := authorize(ctx, options, ToolMemoryBackup); err != nil {
				return nil, memorybackup.Response{}, publicError(err)
			}
			if result, ok, err := ensureExportDestination(req, options.RuntimeAccess, input.Destination); err != nil || !ok {
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
		})
	}
	if options.RuntimeAccess != nil {
		destructive := true
		mcp.AddTool(server, &mcp.Tool{Name: ToolRuntimeAccess, Description: "Inspect, grant with explicit user approval, or revoke ephemeral stdio access without configuration edits or restart.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, input runtimeaccess.Request) (*mcp.CallToolResult, runtimeaccess.Response, error) {
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
				if req.Params.RequestState == "" {
					token, message, err := options.RuntimeAccess.Begin(input.Kind, input.Path)
					if err != nil {
						return nil, runtimeaccess.Response{}, publicError(err)
					}
					return approvalResult(token, message), runtimeaccess.Response{}, nil
				}
				output, err := completeApproval(req, options.RuntimeAccess)
				return nil, output, publicError(err)
			default:
				return nil, runtimeaccess.Response{}, publicError(runtimeaccess.ErrInvalid)
			}
		})
	}
	if options.RuntimeConfig != nil && options.RuntimeAccess != nil {
		destructive := true
		mcp.AddTool(server, &mcp.Tool{Name: ToolRuntimeConfig, Description: "Inspect or change live sources, workspace, state, or HTTP without restarting stdio.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, input runtimeconfig.Request) (*mcp.CallToolResult, runtimeconfig.Response, error) {
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
			if req.Params.RequestState == "" {
				token, message, err := options.RuntimeAccess.BeginConfig(input)
				if err != nil {
					return nil, runtimeconfig.Response{}, publicError(err)
				}
				return approvalResult(token, message), runtimeconfig.Response{}, nil
			}
			accepted, err := approvalAccepted(req)
			if err != nil {
				return nil, runtimeconfig.Response{}, publicError(err)
			}
			output, err := options.RuntimeAccess.CompleteConfig(ctx, req.Params.RequestState, accepted, options.RuntimeConfig)
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

const approvalInputID = "approval"

func approvalResult(token, message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{approvalInputID: &mcp.ElicitParams{
			Mode:    "form",
			Message: message,
			RequestedSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		}},
		RequestState: token,
	}
}

func approvalAccepted(req *mcp.CallToolRequest) (bool, error) {
	response, ok := req.Params.InputResponses[approvalInputID]
	if !ok {
		return false, runtimeaccess.ErrPending
	}
	elicited, ok := response.(*mcp.ElicitResult)
	if !ok {
		return false, runtimeaccess.ErrPending
	}
	return elicited.Action == "accept", nil
}

func completeApproval(req *mcp.CallToolRequest, access *runtimeaccess.Service) (runtimeaccess.Response, error) {
	if access == nil || req.Params.RequestState == "" {
		return runtimeaccess.Response{}, runtimeaccess.ErrPending
	}
	accepted, err := approvalAccepted(req)
	if err != nil {
		return runtimeaccess.Response{}, err
	}
	return access.Complete(req.Params.RequestState, accepted)
}

func resumeApproval(req *mcp.CallToolRequest, access *runtimeaccess.Service) (*mcp.CallToolResult, bool, error) {
	if req.Params.RequestState == "" {
		return nil, true, nil
	}
	_, err := completeApproval(req, access)
	return nil, err == nil, err
}

func ensureCapability(req *mcp.CallToolRequest, access *runtimeaccess.Service, kind string) (*mcp.CallToolResult, bool, error) {
	if access == nil {
		return nil, true, nil
	}
	if req.Params.RequestState != "" {
		if _, err := completeApproval(req, access); err != nil {
			return nil, false, err
		}
	}
	if access.Enabled(kind) {
		return nil, true, nil
	}
	token, message, err := access.Begin(kind, "")
	if err != nil {
		return nil, false, err
	}
	return approvalResult(token, message), false, nil
}

func requestReadApproval[T any](access *runtimeaccess.Service, path string) (*mcp.CallToolResult, T, error) {
	var zero T
	if access == nil {
		return nil, zero, workspace.ErrEscape
	}
	token, message, err := access.Begin(runtimeaccess.KindReadPath, path)
	if err != nil {
		return nil, zero, err
	}
	return approvalResult(token, message), zero, nil
}

func ensureExportDestination(req *mcp.CallToolRequest, access *runtimeaccess.Service, path string) (*mcp.CallToolResult, bool, error) {
	if access == nil {
		return nil, true, nil
	}
	if req.Params.RequestState != "" {
		if _, err := completeApproval(req, access); err != nil {
			return nil, false, err
		}
	}
	if access.GrantedPath(runtimeaccess.KindMemoryExport, path) {
		return nil, true, nil
	}
	token, message, err := access.Begin(runtimeaccess.KindMemoryExport, path)
	if err != nil {
		return nil, false, err
	}
	return approvalResult(token, message), false, nil
}

func sourcePath(ref string) string {
	if strings.HasPrefix(ref, "source:mutable:") {
		return strings.TrimPrefix(ref, "source:mutable:")
	}
	if strings.HasPrefix(ref, "source:") {
		value := strings.TrimPrefix(ref, "source:")
		if marker := strings.LastIndex(value, "@sha256:"); marker >= 0 {
			return value[:marker]
		}
	}
	return ""
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
	case errors.Is(err, runtimeaccess.ErrDisabled):
		code = "runtime_access_unavailable"
	case errors.Is(err, runtimeaccess.ErrDeclined):
		code = "permission_denied"
	case errors.Is(err, runtimeaccess.ErrPending):
		code = "runtime_approval_invalid"
	case errors.Is(err, runtimeconfig.ErrUnavailable):
		code = "runtime_configuration_unavailable"
	case errors.Is(err, runtimeconfig.ErrBusy):
		code = "runtime_configuration_busy"
	case errors.Is(err, memorybackup.ErrDestination):
		code = "backup_destination_invalid"
	case errors.Is(err, memorybackup.ErrNotIdle):
		code = "runner_busy"
	case errors.Is(err, memorybackup.ErrArchive):
		code = "backup_invalid"
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
