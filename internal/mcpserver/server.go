// Package mcpserver adapts RepoPlane services to the Model Context Protocol.
package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync/atomic"
	"time"

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

var fallbackErrorID atomic.Uint64

type publicFailure struct {
	Code          string         `json:"code"`
	Message       string         `json:"message"`
	CorrelationID string         `json:"correlation_id"`
	MutationState string         `json:"mutation_state,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
}

type publicClientError struct{ failure publicFailure }

func (e *publicClientError) Error() string {
	encoded, _ := json.Marshal(e.failure)
	return string(encoded)
}

// New constructs a RepoPlane MCP server. Tool registration is added by the
// feature milestones; constructing the server itself has no workspace side
// effects.
type Options struct {
	Surface string
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
	Authorize      func(context.Context, string) error
	AuditOperation func(context.Context, string, string) error
	ObserveUsage   func(context.Context, store.UsageEvent)
}

func New(version string, provided ...Options) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version}, nil)
	var options Options
	if len(provided) > 0 {
		options = provided[0]
	}
	operations := operationRegistry(options)
	if options.Surface == SurfaceToolboxV1 {
		registerToolboxes(server, options, operations)
		return server
	}
	for _, operation := range operations {
		operation.register(server)
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

func ensureCapability(req *mcp.CallToolRequest, access *runtimeaccess.Service, kind, operation string) (*mcp.CallToolResult, bool, error) {
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
	return approvalResult(token, approvalMessage(operation, message)), false, nil
}

func requestReadApproval[T any](access *runtimeaccess.Service, path, operation string) (*mcp.CallToolResult, T, error) {
	var zero T
	if access == nil {
		return nil, zero, workspace.ErrEscape
	}
	token, message, err := access.Begin(runtimeaccess.KindReadPath, path)
	if err != nil {
		return nil, zero, err
	}
	return approvalResult(token, approvalMessage(operation, message)), zero, nil
}

func ensureExportDestination(req *mcp.CallToolRequest, access *runtimeaccess.Service, path, operation string) (*mcp.CallToolResult, bool, error) {
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
	return approvalResult(token, approvalMessage(operation, message)), false, nil
}

func approvalMessage(operation, message string) string {
	return "Operation " + operation + ": " + message
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
	return publicErrorWithMutationState(err, false)
}

func publicMutationError(err error) error {
	return publicErrorWithMutationState(err, true)
}

func publicErrorWithMutationState(err error, mutation bool) error {
	if err == nil {
		return nil
	}
	var existing *publicClientError
	if errors.As(err, &existing) {
		return err
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
	case errors.Is(err, records.ErrInvalidArgument):
		code = "invalid_argument"
	case errors.Is(err, records.ErrInvalidTransition):
		code = "invalid_transition"
	case errors.Is(err, records.ErrStorageFailure):
		code = "storage_failure"
	case errors.Is(err, records.ErrPermissionDenied):
		code = "permission_denied"
	case errors.Is(err, ErrAuthorizationDenied):
		code = "permission_denied"
	case errors.Is(err, records.ErrReportInvalid):
		code = "report_invalid"
	case errors.Is(err, runner.ErrNotExecutable):
		code = "capability_not_executable"
	case errors.Is(err, runner.ErrUnsupportedScriptType):
		code = "unsupported_script_type"
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
	correlationID := newErrorCorrelationID()
	mutationState := ""
	if mutation {
		mutationState = "not_applied"
		if code == "storage_failure" || code == "internal_error" || code == "deadline_exceeded" {
			mutationState = "unknown"
		}
	}
	if code == "storage_failure" || code == "internal_error" {
		_, _ = fmt.Fprintf(os.Stderr, "repoplane: correlation_id=%s code=%s error=%v\n", correlationID, code, err)
	}
	return &publicClientError{failure: publicFailure{
		Code: code, Message: publicErrorMessage(code, err), CorrelationID: correlationID, MutationState: mutationState,
		Details: publicErrorDetails(err),
	}}
}

func publicErrorDetails(err error) map[string]any {
	var limit *runner.PatternLimitError
	if !errors.As(err, &limit) {
		return nil
	}
	details := map[string]any{
		"resource": limit.Resource, "limit_kind": limit.LimitKind, "maximum": limit.Maximum,
		"observed_lower_bound": limit.ObservedLowerBound,
		"ignored_path_policy":  "included",
		"build_output_policy":  "included",
		"hint":                 "use narrower source-only globs and exclude build outputs",
	}
	if limit.Pattern != "" {
		details["pattern"] = limit.Pattern
	}
	return details
}

func newErrorCorrelationID() string {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err == nil {
		return "err_" + hex.EncodeToString(data)
	}
	return fmt.Sprintf("err_%x_%x", time.Now().UTC().UnixNano(), fallbackErrorID.Add(1))
}

func publicErrorMessage(code string, err error) string {
	switch code {
	case "invalid_argument":
		var validation *records.ValidationError
		if errors.As(err, &validation) {
			return "validation failed: " + validation.Error()
		}
		return "request validation failed"
	case "invalid_transition":
		return "requested record transition is not allowed"
	case "revision_conflict":
		return "expected revision does not match the current record"
	case "storage_failure":
		return "durable storage operation failed"
	case "record_not_found":
		return "record was not found"
	case "internal_error":
		return "internal operation failed"
	case "unsupported_script_type":
		return "direct PowerShell script execution is unsupported; use a PowerShell executable_ref and pass the workspace-relative script path in argv_template"
	default:
		return strings.ReplaceAll(code, "_", " ")
	}
}

// RunStdio serves MCP frames on stdin/stdout until the client disconnects or
// ctx is cancelled. Diagnostics must never be written to stdout.
func RunStdio(ctx context.Context, version string, options ...Options) error {
	return New(version, options...).Run(ctx, &mcp.StdioTransport{})
}
