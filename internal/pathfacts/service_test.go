package pathfacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"repoplane/internal/search"
	"repoplane/internal/textcodec"
	"repoplane/internal/workspace"
)

type searchStub struct {
	outcome search.BackendOutcome
	err     error
}

func (s searchStub) Search(context.Context, search.BackendQuery) (search.BackendOutcome, error) {
	return s.outcome, s.err
}

func TestExplainEncodingNewlinesRulesAndBasenames(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(rootPath, "AGENTS.md"), filepath.Join(rootPath, "sub", "AGENTS.md")} {
		if err := os.WriteFile(path, []byte("rules\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := textcodec.Encode("cp949", "설정\r\n검사\n끝\r")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "sub", "target.txt"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := workspace.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(root, searchStub{outcome: search.BackendOutcome{
		Matches: []search.Match{{Path: "sub/target.txt"}, {Path: "copy/target.txt"}}, Complete: true,
	}}, []string{"AGENTS.md"})
	response, err := service.Explain(context.Background(), Request{Path: "sub/target.txt", Encoding: "cp949"})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 {
		t.Fatalf("items=%d", len(response.Items))
	}
	facts := response.Items[0]
	if facts.Kind != "file" || facts.ResolvedKind != "file" || !facts.Exists || facts.Encoding.DecodeSuccess == nil || !*facts.Encoding.DecodeSuccess || facts.Encoding.ByteRoundTrip == nil || !*facts.Encoding.ByteRoundTrip {
		t.Fatalf("unexpected file/encoding facts: %+v", facts)
	}
	if facts.Newlines.CRLF != 1 || facts.Newlines.LF != 1 || facts.Newlines.CR != 1 || facts.Newlines.FinalNewline == nil || !*facts.Newlines.FinalNewline {
		t.Fatalf("unexpected newline facts: %+v", facts.Newlines)
	}
	if len(facts.Rules) != 2 || len(facts.BasenameCandidates) != 2 || facts.ContentHash == "" {
		t.Fatalf("unexpected rules/candidates/hash: %+v", facts)
	}
}

func TestExplainMissingAndEscape(t *testing.T) {
	root, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(root, searchStub{outcome: search.BackendOutcome{Complete: true}}, nil)
	response, err := service.Explain(context.Background(), Request{Path: "missing/file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Items[0].Exists || response.Items[0].Kind != "missing" {
		t.Fatalf("unexpected missing facts: %+v", response.Items[0])
	}
	if _, err := service.Explain(context.Background(), Request{Path: "../outside.txt"}); !errors.Is(err, workspace.ErrEscape) {
		t.Fatalf("error=%v, want workspace.ErrEscape", err)
	}
}

func TestExplainReportsRequestedSymlink(t *testing.T) {
	rootPath := t.TempDir()
	target := filepath.Join(rootPath, "target.txt")
	if err := os.WriteFile(target, []byte("text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(rootPath, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	root, err := workspace.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(root, searchStub{outcome: search.BackendOutcome{Complete: true}}, nil)
	response, err := service.Explain(context.Background(), Request{Path: "link.txt"})
	if err != nil {
		t.Fatal(err)
	}
	facts := response.Items[0]
	if facts.Kind != "symlink" || facts.ResolvedKind != "file" || !facts.Links.Symlink {
		t.Fatalf("unexpected symlink facts: %+v", facts)
	}
}

func TestExplainReportsHardlinkCount(t *testing.T) {
	rootPath := t.TempDir()
	target := filepath.Join(rootPath, "target.txt")
	if err := os.WriteFile(target, []byte("text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(rootPath, "alias.txt")); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	root, err := workspace.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(root, searchStub{outcome: search.BackendOutcome{Complete: true}}, nil)
	response, err := service.Explain(context.Background(), Request{Path: "alias.txt"})
	if err != nil {
		t.Fatal(err)
	}
	count := response.Items[0].Links.HardlinkCount
	if count == nil || *count < 2 {
		t.Fatalf("hardlink count=%v", count)
	}
}

func TestExplainMarksLargeInspectionPartial(t *testing.T) {
	rootPath := t.TempDir()
	data := make([]byte, MaxInspectBytes+1)
	for index := range data {
		data[index] = 'a'
	}
	if err := os.WriteFile(filepath.Join(rootPath, "large.txt"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := workspace.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(root, searchStub{outcome: search.BackendOutcome{Complete: true}}, nil)
	response, err := service.Explain(context.Background(), Request{Path: "large.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "partial" || !response.Items[0].Encoding.ObservationPartial || response.Items[0].ContentHash != "" || response.Items[0].Newlines.FinalNewline != nil {
		t.Fatalf("unexpected large-file response: %+v", response)
	}
}

func TestExplainUTF8BOMPreservesByteObservation(t *testing.T) {
	rootPath := t.TempDir()
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte("text\n")...)
	if err := os.WriteFile(filepath.Join(rootPath, "bom.txt"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := workspace.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(root, searchStub{outcome: search.BackendOutcome{Complete: true}}, nil)
	response, err := service.Explain(context.Background(), Request{Path: "bom.txt", Encoding: "utf-8"})
	if err != nil {
		t.Fatal(err)
	}
	encoding := response.Items[0].Encoding
	if encoding.BOM != "utf-8" || encoding.DecodeSuccess == nil || !*encoding.DecodeSuccess || encoding.ByteRoundTrip == nil || *encoding.ByteRoundTrip {
		t.Fatalf("unexpected BOM observation: %+v", encoding)
	}
}
