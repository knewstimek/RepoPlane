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
	"repoplane/internal/search"
	"repoplane/internal/textcodec"
	"repoplane/internal/workspace"
)

const serverName = "repoplane"

// New constructs a RepoPlane MCP server. Tool registration is added by the
// feature milestones; constructing the server itself has no workspace side
// effects.
type Options struct {
	Catalog   *catalog.Service
	Search    *search.Service
	PathFacts *pathfacts.Service
	DataQuery *dataquery.Service
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
	case errors.Is(err, contracts.ErrLimitExceeded),
		errors.Is(err, catalog.ErrResponseTooLarge), errors.Is(err, search.ErrResponseTooLarge),
		errors.Is(err, pathfacts.ErrResponseTooLarge), errors.Is(err, dataquery.ErrResponseTooLarge),
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
