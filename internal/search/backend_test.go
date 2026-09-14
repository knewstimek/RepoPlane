package search

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"repoplane/internal/textcodec"
)

func newRGTestBackend(t *testing.T) *RGBackend {
	t.Helper()
	backend, err := NewRGBackend(context.Background())
	if err != nil {
		t.Skipf("ripgrep unavailable: %v", err)
	}
	return backend
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
