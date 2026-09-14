//go:build windows

package runner

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"repoplane/internal/catalog"
)

func TestWindowsRegisteredBatchWrapperPreservesArgumentBoundary(t *testing.T) {
	manifest := catalog.Manifest{
		ID: "test.wrapper", Revision: 1, Summary: "Windows wrapper",
		Execution: &catalog.Execution{Kind: "cli", ExecutableRef: "tools/wrapper.cmd", CWD: ".", ArgvTemplate: []string{"{message}"}, TrustedForRun: true, TimeoutSec: 5},
		Arguments: map[string]catalog.Argument{"message": {Type: "string", Required: true}},
	}
	service, _, cleanup := newTestService(t, manifest)
	defer cleanup()
	script := "@echo off\r\nset \"REPOPLANE_WRAPPER_ARG=%~1\"\r\ncall \"%~dp0repoplane-runner-test.cmd\"\r\n"
	if err := os.WriteFile(filepath.Join(service.root.Resolved(), "tools", "wrapper.cmd"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REPOPLANE_RUNNER_HELPER", "1")
	assertWrapperOutput(t, service, "공백 값 & 안전")
}

func assertWrapperOutput(t *testing.T, service *Service, message string) {
	t.Helper()
	prepared, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: "test.wrapper", CapabilityRevision: "1", Arguments: map[string]any{"message": message}})
	if err != nil {
		t.Fatal(err)
	}
	executed, err := service.Execute(context.Background(), ExecuteRequest{PlanID: prepared.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	result := awaitTerminal(t, service, executed.RunID)
	if result.Run["state"] != "completed" {
		t.Fatalf("wrapper failed: %+v", result.Run)
	}
	stream, err := service.Inspect(context.Background(), InspectRequest{RunID: executed.RunID, Action: "stdout"})
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(stream.Stream.BytesBase64)
	if strings.TrimSpace(string(decoded)) != message {
		t.Fatalf("wrapper argument boundary lost: got %q want %q", decoded, message)
	}
}

func writePlatformHelper(t *testing.T, destination string) {
	t.Helper()
	script := "@echo off\r\nif \"%REPOPLANE_RUNNER_SLEEP%\"==\"1\" powershell.exe -NoProfile -NonInteractive -Command \"Start-Sleep -Seconds 10\"\r\nif defined REPOPLANE_WRAPPER_ARG (powershell.exe -NoProfile -NonInteractive -Command \"[Console]::Out.Write($env:REPOPLANE_WRAPPER_ARG)\") else (echo runner stdout)\r\n>&2 echo runner stderr\r\nif \"%REPOPLANE_RUNNER_OUTPUT%\"==\"1\" >out.txt echo artifact\r\nif defined REPOPLANE_RUNNER_EXIT exit /b %REPOPLANE_RUNNER_EXIT%\r\nexit /b 0\r\n"
	if err := os.WriteFile(destination, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
}
