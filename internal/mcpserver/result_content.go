package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"repoplane/internal/resultview"
)

// Receiving middleware runs after the SDK has populated structuredContent and
// its compatibility echo. It covers typed and toolbox calls, for either transport.
func deduplicateToolResults(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, request)
		if err != nil || method != "tools/call" {
			return result, err
		}
		toolResult, ok := result.(*mcp.CallToolResult)
		if !ok || toolResult == nil || toolResult.StructuredContent == nil ||
			len(toolResult.Content) == 0 || toolResult.InputRequests != nil {
			return result, nil
		}
		structured, err := json.Marshal(toolResult.StructuredContent)
		if err != nil {
			return result, nil
		}
		blocks := make([]json.RawMessage, len(toolResult.Content))
		for i, block := range toolResult.Content {
			// An unmarshalable or ambiguous block cannot be marked as an echo.
			blocks[i], _ = json.Marshal(block)
		}
		duplicates := resultview.DuplicateTextBlocks(structured, blocks)
		kept := make([]mcp.Content, 0, len(blocks))
		removed := false
		textBytes := 0
		for i, block := range toolResult.Content {
			if duplicates[i] {
				removed = true
				continue
			}
			kept = append(kept, block)
			if text, ok := block.(*mcp.TextContent); ok {
				textBytes += len(text.Text)
			}
		}
		if !removed {
			return result, nil
		}
		// Keep the original typed payload, error state, approvals and metadata.
		// A JSON round trip here could round large numeric fields.
		filtered := *toolResult
		filtered.Content = kept
		if usage, ok := toolResult.Meta["repoplane/usage.v1"].(map[string]any); ok {
			filtered.Meta = make(mcp.Meta, len(toolResult.Meta))
			for key, value := range toolResult.Meta {
				filtered.Meta[key] = value
			}
			actual := make(map[string]any, len(usage))
			for key, value := range usage {
				actual[key] = value
			}
			actual["structured_bytes"] = len(structured)
			actual["text_bytes"] = textBytes
			filtered.Meta["repoplane/usage.v1"] = actual
		}
		return &filtered, nil
	}
}
