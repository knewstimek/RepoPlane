package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDeduplicateToolResultsPreservesOriginalTypesAndUniqueContent(t *testing.T) {
	body := json.RawMessage(`{"id":"9007199254740993","n":9007199254740993,"warnings":[{"code":"verify_source"}]}`)
	originalError := errors.New("review before use")
	annotated := &mcp.TextContent{Text: string(body), Meta: mcp.Meta{"audience": "user"}}
	image := &mcp.ImageContent{Data: []byte{0}, MIMEType: "image/png"}
	original := &mcp.CallToolResult{
		StructuredContent: body,
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(body)},
			&mcp.TextContent{Text: "고유 설명"},
			annotated, image,
			&mcp.TextContent{Text: `{"id":"9007199254740993","n":9007199254740992,"warnings":[{"code":"verify_source"}]}`},
		},
		Meta:         mcp.Meta{"repoplane/usage.v1": map[string]any{"text_bytes": 999, "structured_bytes": 999, "server_duration_ms": 1.25}, "extension": "keep"},
		RequestState: "opaque-state",
	}
	original.SetError(originalError)
	handler := deduplicateToolResults(func(context.Context, string, mcp.Request) (mcp.Result, error) { return original, nil })
	result, err := handler(context.Background(), "tools/call", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := result.(*mcp.CallToolResult)
	if len(got.Content) != 4 || got.Content[1] != annotated || got.Content[2] != image ||
		!got.IsError || got.GetError() != originalError || got.RequestState != original.RequestState ||
		!reflect.DeepEqual(got.StructuredContent, body) || got.Meta["extension"] != "keep" {
		t.Fatalf("nonredundant data changed: %+v", got)
	}
	actualBytes := 0
	for _, block := range got.Content {
		if text, ok := block.(*mcp.TextContent); ok {
			actualBytes += len(text.Text)
		}
	}
	usage := got.Meta["repoplane/usage.v1"].(map[string]any)
	if usage["text_bytes"] != actualBytes || usage["structured_bytes"] != len(body) || usage["server_duration_ms"] != 1.25 {
		t.Fatalf("actual wire usage not preserved: %+v", usage)
	}
	if len(original.Content) != 5 || original.Meta["repoplane/usage.v1"].(map[string]any)["text_bytes"] != 999 {
		t.Fatal("shared result mutated")
	}
	encoded, err := json.Marshal(got)
	if err != nil || !strings.Contains(string(encoded), `"n":9007199254740993`) {
		t.Fatalf("exact number lost: %s %v", encoded, err)
	}
}

func TestDeduplicateToolResultsLeavesApprovalsAndProtocolErrors(t *testing.T) {
	approval := approvalResult("opaque-state", "Review first")
	// Even an unusual mixed result must retain its approval handshake.
	approval.StructuredContent = json.RawMessage(`{"state":"pending"}`)
	approval.Content = []mcp.Content{&mcp.TextContent{Text: `{"state":"pending"}`}}
	for _, method := range []string{"tools/call", "initialize"} {
		handler := deduplicateToolResults(func(context.Context, string, mcp.Request) (mcp.Result, error) { return approval, nil })
		got, err := handler(context.Background(), method, nil)
		if err != nil || got != approval || len(approval.Content) != 1 {
			t.Fatal("approval/non-tool result was changed")
		}
	}
	sentinel := errors.New("protocol error")
	handler := deduplicateToolResults(func(context.Context, string, mcp.Request) (mcp.Result, error) { return nil, sentinel })
	if _, err := handler(context.Background(), "tools/call", nil); err != sentinel {
		t.Fatal("protocol error changed")
	}
}

func TestAutomaticResultContentModesAfterSDKProcessing(t *testing.T) {
	for _, mode := range []string{"compatible", "deduplicated"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			session := connectTestClient(t, ctx, Options{ResultContent: mode, Catalog: &catalogToolboxStub{}, Records: recordBriefStub{}})
			defer session.Close()
			if !strings.Contains(session.InitializeResult().Instructions, "Result content mode: "+mode) {
				t.Fatal("effective mode missing from initialization")
			}
			// The SDK creates the catalog JSON text echo. No render command is invoked.
			full, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolCatalogQuery, Arguments: map[string]any{"mode": "status"}})
			if err != nil || full.IsError || full.StructuredContent == nil {
				t.Fatalf("full result=%+v err=%v", full, err)
			}
			if mode == "compatible" && len(full.Content) == 0 || mode == "deduplicated" && len(full.Content) != 0 {
				t.Fatalf("automatic content mode failed: %+v", full)
			}
			brief, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolProjectRecords, Arguments: map[string]any{"query": "example"}})
			if err != nil || brief.IsError || len(brief.Content) != 1 ||
				brief.Content[0].(*mcp.TextContent).Text != "memo_example Example: Short context" {
				t.Fatalf("brief fallback lost: %+v %v", brief, err)
			}
			failure, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolCatalogQuery, Arguments: map[string]any{"mode": "invalid"}})
			if err != nil || !failure.IsError || len(failure.Content) == 0 {
				t.Fatalf("tool error lost: %+v %v", failure, err)
			}
		})
	}
}
