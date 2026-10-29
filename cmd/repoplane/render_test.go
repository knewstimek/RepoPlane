package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderResultCommand(t *testing.T) {
	var output, diagnostics bytes.Buffer
	input := `{"structuredContent":{"status":"ok"},"content":[{"type":"text","text":"{\"status\":\"ok\"}"}]}`
	if err := runRenderResult(nil, strings.NewReader(input), &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if output.String() != "{\"content\":[],\"structuredContent\":{\"status\":\"ok\"}}\n" || diagnostics.Len() != 0 {
		t.Fatalf("output=%q diagnostics=%q", output.String(), diagnostics.String())
	}
}

func TestRenderResultCommandRejectsOversizeAndInvalidInputWithoutPartialOutput(t *testing.T) {
	for _, input := range []string{"{}", strings.Repeat(" ", renderInputLimit+1)} {
		var output, diagnostics bytes.Buffer
		if err := runRenderResult(nil, strings.NewReader(input), &output, &diagnostics); err == nil || output.Len() != 0 {
			t.Fatalf("err=%v output=%q", err, output.String())
		}
	}
}

func TestRenderResultHelpDoesNotReadInput(t *testing.T) {
	var output, diagnostics bytes.Buffer
	if err := runRenderResult([]string{"--help"}, nil, &output, &diagnostics); err != nil ||
		output.Len() != 0 || !strings.Contains(diagnostics.String(), "16 MiB") {
		t.Fatalf("err=%v output=%q diagnostics=%q", err, output.String(), diagnostics.String())
	}
}
