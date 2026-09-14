package devtool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReportFinishPreservesPartialState(t *testing.T) {
	report := Report{
		SchemaVersion: SchemaVersion,
		Operation:     "verify",
		StartedAt:     time.Now().UTC(),
		Checks: []Check{
			{ID: "pass", Status: "passed"},
			{ID: "unknown", Status: "unknown", Truncated: true},
		},
	}
	report.Finish()
	if report.Status != "partial" || !report.Truncated {
		t.Fatalf("Finish() status=%q truncated=%v", report.Status, report.Truncated)
	}
	if report.Counts.Passed != 1 || report.Counts.Unknown != 1 {
		t.Fatalf("Finish() counts=%+v", report.Counts)
	}
}

func TestWriteReportIsBoundedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "report.json")
	report := Report{SchemaVersion: SchemaVersion, Operation: "preflight", Status: "passed", StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()}
	if err := WriteReport(path, report); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version=%q", decoded.SchemaVersion)
	}
}

func TestPreflightDoesNotReturnEnvironmentValue(t *testing.T) {
	const name = "REPOPLANE_TEST_REQUIRED_VALUE"
	const secret = "nonsecret-fixture-value"
	t.Setenv(name, secret)
	report, err := Preflight(context.Background(), Options{Root: t.TempDir(), RequiredEnv: []string{name}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("preflight report exposed an environment value")
	}
}

func TestWorkspaceRelativeRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := workspaceRelative(root, filepath.Join("..", "outside")); err == nil {
		t.Fatal("workspaceRelative accepted an escape")
	}
	if _, err := workspaceRelative(root, "go.mod"); err != nil {
		t.Fatalf("workspaceRelative rejected safe path: %v", err)
	}
}

func TestPublicPatternsDetectStrongIndicatorsWithoutReturningContent(t *testing.T) {
	privateKey := "-----BEGIN " + "PRIVATE KEY-----"
	githubToken := "gh" + "p_1234567890abcdef"
	accessKey := "AK" + "IA1234567890ABCDEF"
	for _, value := range []string{privateKey, githubToken, accessKey} {
		if !hasPublicFinding([]byte(value)) {
			t.Fatalf("strong indicator was not detected")
		}
	}
	if hasPublicFinding([]byte("token: EXAMPLE")) {
		t.Fatal("neutral placeholder produced a finding")
	}
}
