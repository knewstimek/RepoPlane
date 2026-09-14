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
	if got.EnableIntentionWrites || got.EnableReportImport {
		t.Fatal("record mutation flags must default to disabled")
	}
}

func TestParseEnablesRecordCapabilitiesExplicitly(t *testing.T) {
	got, err := Parse([]string{"--enable-intention-writes", "--enable-report-import"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !got.EnableIntentionWrites || !got.EnableReportImport {
		t.Fatalf("settings=%+v", got)
	}
}
