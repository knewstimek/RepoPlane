package search

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"repoplane/internal/textcodec"
	"repoplane/internal/workspace"
)

func newRGTestBackend(t *testing.T) *RGBackend {
	t.Helper()
	backend, err := NewRGBackend(context.Background())
	if err != nil {
		t.Skipf("ripgrep unavailable: %v", err)
	}
	return backend
}

func TestAdapterBackendSymbolAndGitHistory(t *testing.T) {
	root := t.TempDir()
	writeSearchFile(t, root, "symbols.jsonl", `{"_type":"tag","name":"FindThing","kind":"function","language":"Go","path":"main.go","line":7}`+"\n")
	workspaceRoot, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	resolvedRoot := workspaceRoot.Resolved()
	base := newRGTestBackend(t)
	backend := NewAdapterBackend(context.Background(), base, resolvedRoot, []string{"symbols.jsonl"})
	symbols, err := backend.Search(context.Background(), BackendQuery{Mode: "symbol", Pattern: "Find", Directory: resolvedRoot})
	if err != nil || len(symbols.Matches) != 1 || symbols.Matches[0].Channel != "prefix" || symbols.Matches[0].Validity != "unknown" {
		t.Fatalf("symbols=%+v err=%v", symbols, err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	run := func(args ...string) {
		command := exec.Command(git, args...)
		command.Dir = resolvedRoot
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=example@example.invalid", "GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=example@example.invalid")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	run("init", "-q")
	writeSearchFile(t, root, "main.go", "package main\n// history needle\n")
	run("add", "main.go")
	run("commit", "-q", "-m", "add searchable history")
	writeSearchFile(t, root, "sub/nested.go", "package sub\n")
	run("add", "sub/nested.go")
	run("commit", "-q", "-m", "add nested file")
	history, err := backend.Search(context.Background(), BackendQuery{Mode: "git_history", Pattern: "searchable", MatchKind: "commit", Directory: resolvedRoot, CaseSensitive: false})
	if err != nil || len(history.Matches) != 1 || history.Matches[0].Channel != "commit" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	paths, err := backend.Search(context.Background(), BackendQuery{Mode: "git_history", Pattern: "main.go", MatchKind: "path", Directory: resolvedRoot, CaseSensitive: true})
	if err != nil || len(paths.Matches) != 1 || paths.Matches[0].Path != "main.go" {
		t.Fatalf("paths=%+v err=%v", paths, err)
	}
	diffs, err := backend.Search(context.Background(), BackendQuery{Mode: "git_history", Pattern: "history needle", MatchKind: "diff", Directory: resolvedRoot, CaseSensitive: true})
	if err != nil || len(diffs.Matches) != 1 || diffs.Matches[0].Channel != "diff" {
		t.Fatalf("diffs=%+v err=%v", diffs, err)
	}
	nested, err := backend.Search(context.Background(), BackendQuery{Mode: "git_history", Pattern: "nested.go", MatchKind: "path", Directory: filepath.Join(resolvedRoot, "sub"), CaseSensitive: true})
	if err != nil || len(nested.Matches) != 1 || nested.Matches[0].Path != "nested.go" {
		t.Fatalf("nested=%+v err=%v", nested, err)
	}
}

func writeSearchFile(t *testing.T, root, relative, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRGBackendFilenameAndTextModes(t *testing.T) {
	root := t.TempDir()
	writeSearchFile(t, root, "src/file with space.txt", "Alpha needle\nsecond needle\n")
	writeSearchFile(t, root, "src/other.go", "package example\n")
	writeSearchFile(t, root, "generated/ignored.txt", "needle\n")
	writeSearchFile(t, root, "vendor/ignored.txt", "needle\n")
	backend := newRGTestBackend(t)

	files, err := backend.Search(context.Background(), BackendQuery{
		Mode: "filename", Pattern: "space", Directory: root,
		Ignored: "exclude", Generated: "exclude", Vendor: "exclude", Encoding: "utf-8",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !files.Complete || len(files.Matches) != 1 || files.Matches[0].Path != "src/file with space.txt" {
		t.Fatalf("unexpected filename matches: %+v", files)
	}

	exact, err := backend.Search(context.Background(), BackendQuery{
		Mode: "exact", Pattern: "needle", Directory: root,
		Ignored: "exclude", Generated: "exclude", Vendor: "exclude", Encoding: "utf-8", CaseSensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !exact.Complete || len(exact.Matches) != 2 || exact.Matches[0].Line != 1 || exact.Matches[1].Line != 2 {
		t.Fatalf("unexpected exact matches: %+v", exact)
	}

	regex, err := backend.Search(context.Background(), BackendQuery{
		Mode: "regex", Pattern: `(?i)alpha\s+needle`, Directory: root,
		Ignored: "exclude", Generated: "exclude", Vendor: "exclude", Encoding: "utf-8", CaseSensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(regex.Matches) != 1 || regex.Matches[0].Line != 1 {
		t.Fatalf("unexpected regex matches: %+v", regex)
	}
}

func TestRGBackendRejectsInvalidRegex(t *testing.T) {
	backend := newRGTestBackend(t)
	_, err := backend.Search(context.Background(), BackendQuery{
		Mode: "regex", Pattern: "[", Directory: t.TempDir(),
		Ignored: "exclude", Generated: "exclude", Vendor: "exclude", Encoding: "utf-8",
	})
	if err == nil {
		t.Fatal("invalid regex succeeded")
	}
}

func TestRGBackendDistinguishesCP949AndStrictEUCKR(t *testing.T) {
	root := t.TempDir()
	cp949, err := textcodec.Encode("cp949", "갂 확장 문자\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cp949.txt"), cp949, 0o600); err != nil {
		t.Fatal(err)
	}
	euc, err := textcodec.Encode("euc-kr", "설정 검사\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "euc.txt"), euc, 0o600); err != nil {
		t.Fatal(err)
	}
	backend := newRGTestBackend(t)

	cpResult, err := backend.Search(context.Background(), BackendQuery{
		Mode: "exact", Pattern: "갂", Directory: root,
		Ignored: "exclude", Generated: "exclude", Vendor: "exclude", Encoding: "cp949", CaseSensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cpResult.Matches) != 1 || cpResult.Matches[0].Path != "cp949.txt" {
		t.Fatalf("unexpected CP949 result: %+v", cpResult)
	}

	eucResult, err := backend.Search(context.Background(), BackendQuery{
		Mode: "exact", Pattern: "설정", Directory: root,
		Ignored: "exclude", Generated: "exclude", Vendor: "exclude", Encoding: "euc-kr", CaseSensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if eucResult.Complete || len(eucResult.Matches) != 1 || eucResult.Matches[0].Path != "euc.txt" {
		t.Fatalf("strict EUC-KR should match EUC file and report CP949 decode failure: %+v", eucResult)
	}
}

func TestRGBackendScopePolicies(t *testing.T) {
	root := t.TempDir()
	writeSearchFile(t, root, "visible.txt", "visible\n")
	writeSearchFile(t, root, ".hidden/secret.txt", "hidden\n")
	writeSearchFile(t, root, "generated/output.txt", "generated\n")
	writeSearchFile(t, root, "vendor/dependency.txt", "vendor\n")
	writeSearchFile(t, root, "ignored.txt", "ignored\n")
	if err := os.WriteFile(filepath.Join(root, ".ignore"), []byte("ignored.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := newRGTestBackend(t)

	excluded, err := backend.Search(context.Background(), BackendQuery{
		Mode: "filename", Pattern: ".txt", Directory: root,
		Ignored: "exclude", Generated: "exclude", Vendor: "exclude", Encoding: "utf-8",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(excluded.Matches) != 1 || excluded.Matches[0].Path != "visible.txt" {
		t.Fatalf("default exclusions returned %+v", excluded.Matches)
	}

	included, err := backend.Search(context.Background(), BackendQuery{
		Mode: "filename", Pattern: ".txt", Directory: root, Hidden: true,
		Ignored: "include", Generated: "include", Vendor: "include", Encoding: "utf-8",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(included.Matches) != 5 {
		t.Fatalf("include policies returned %d matches: %+v", len(included.Matches), included.Matches)
	}
}
