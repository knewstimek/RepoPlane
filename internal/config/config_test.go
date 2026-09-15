package config

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestParseCatalogRoots(t *testing.T) {
	got, err := Parse([]string{
		"--workspace", "workspace", "--state-dir", "state",
		"--catalog-root", "one", "--catalog-root", "two",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CatalogRoots) != 2 || got.CatalogRoots[0] != "one" || got.CatalogRoots[1] != "two" {
		t.Fatalf("catalog roots=%v", got.CatalogRoots)
	}
}

func TestParseDefaultsCatalogRoot(t *testing.T) {
	got, err := Parse(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CatalogRoots) != 1 || got.CatalogRoots[0] != "catalog" {
		t.Fatalf("catalog roots=%v", got.CatalogRoots)
	}
	if len(got.CandidateRoots) != 2 || got.CandidateRoots[0] != "tools" || got.CandidateRoots[1] != "scripts" {
		t.Fatalf("candidate roots=%v", got.CandidateRoots)
	}
	if len(got.RuleFiles) != 1 || got.RuleFiles[0] != "AGENTS.md" {
		t.Fatalf("rule files=%v", got.RuleFiles)
	}
	if got.EnableIntentionWrites || got.EnableReportImport || got.EnableRunner || got.EnableCache {
		t.Fatal("mutation and execution flags must default to disabled")
	}
}

func TestParseDefaultsCatalogRootToNestedStartupRepository(t *testing.T) {
	workspace := t.TempDir()
	repository := filepath.Join(workspace, "code")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repository, "catalog"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repository)
	got, err := Parse([]string{"--workspace", workspace, "--state-dir", t.TempDir()}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CatalogRoots) != 1 || got.CatalogRoots[0] != "code/catalog" {
		t.Fatalf("catalog roots=%v", got.CatalogRoots)
	}
}

func TestParsePrefersWorkspaceCatalogAndExplicitRoots(t *testing.T) {
	workspace := t.TempDir()
	repository := filepath.Join(workspace, "code")
	for _, path := range []string{filepath.Join(workspace, "catalog"), filepath.Join(repository, ".git"), filepath.Join(repository, "catalog")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(repository)
	got, err := Parse([]string{"--workspace", workspace, "--state-dir", t.TempDir()}, io.Discard)
	if err != nil || len(got.CatalogRoots) != 1 || got.CatalogRoots[0] != "catalog" {
		t.Fatalf("workspace default=%v err=%v", got.CatalogRoots, err)
	}
	explicit, err := Parse([]string{"--workspace", workspace, "--state-dir", t.TempDir(), "--catalog-root", "code/catalog"}, io.Discard)
	if err != nil || len(explicit.CatalogRoots) != 1 || explicit.CatalogRoots[0] != "code/catalog" {
		t.Fatalf("explicit roots=%v err=%v", explicit.CatalogRoots, err)
	}
	if err := os.Remove(filepath.Join(workspace, "catalog")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)
	uniqueChild, err := Parse([]string{"--workspace", workspace, "--state-dir", t.TempDir()}, io.Discard)
	if err != nil || len(uniqueChild.CatalogRoots) != 1 || uniqueChild.CatalogRoots[0] != "code/catalog" {
		t.Fatalf("unique child repository default=%v err=%v", uniqueChild.CatalogRoots, err)
	}
	for _, path := range []string{filepath.Join(workspace, "other", ".git"), filepath.Join(workspace, "other", "catalog")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ambiguous, err := Parse([]string{"--workspace", workspace, "--state-dir", t.TempDir()}, io.Discard)
	if err != nil || len(ambiguous.CatalogRoots) != 1 || ambiguous.CatalogRoots[0] != "catalog" {
		t.Fatalf("ambiguous child repositories were selected: roots=%v err=%v", ambiguous.CatalogRoots, err)
	}
}

func TestParseEnablesRecordCapabilitiesExplicitly(t *testing.T) {
	got, err := Parse([]string{"--enable-intention-writes", "--enable-report-import", "--enable-runner", "--enable-cache"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !got.EnableIntentionWrites || !got.EnableReportImport || !got.EnableRunner || !got.EnableCache {
		t.Fatalf("settings=%+v", got)
	}
}

func TestParseRejectsCacheWithoutRunner(t *testing.T) {
	if _, err := Parse([]string{"--enable-cache"}, io.Discard); err == nil {
		t.Fatal("cache enabled without Runner")
	}
}

func TestHTTPTransportRequiresProfile(t *testing.T) {
	if _, err := Parse([]string{"--transport", "http"}, io.Discard); err == nil {
		t.Fatal("HTTP without profile succeeded")
	}
	settings, err := Parse([]string{"--transport", "http", "--http-profile", "host.yaml"}, io.Discard)
	if err != nil || settings.Transport != "http" || settings.HTTPProfile != "host.yaml" {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
	if _, err := Parse([]string{"--http-profile", "host.yaml"}, io.Discard); err == nil {
		t.Fatal("stdio accepted HTTP profile")
	}
}
