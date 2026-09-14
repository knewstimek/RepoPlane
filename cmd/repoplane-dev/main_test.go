package main

import (
	"bytes"
	"strings"
	"testing"

	"repoplane/internal/devtool"
)

func TestWriteFailureDetailsIsBoundedToNonPassingChecks(t *testing.T) {
	exit := 1
	report := devtool.Report{Checks: []devtool.Check{
		{ID: "passed", Status: "passed", Summary: "completed"},
		{ID: "test.all", Status: "failed", ExitCode: &exit, DurationMS: 25, Summary: "command exited with code 1", Diagnostics: "--- FAIL: TestExample"},
	}}
	var output bytes.Buffer
	writeFailureDetails(&output, report)
	got := output.String()
	if strings.Contains(got, "passed") || !strings.Contains(got, "failed check: test.all") || !strings.Contains(got, "--- FAIL: TestExample") {
		t.Fatalf("unexpected failure details %q", got)
	}
}
