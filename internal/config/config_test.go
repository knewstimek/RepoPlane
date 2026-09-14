package config

import (
	"io"
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
