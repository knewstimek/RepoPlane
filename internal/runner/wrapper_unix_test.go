//go:build !windows

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

func TestPOSIXRegisteredShebangPreservesArgumentBoundary(t *testing.T) {
	manifest := catalog.Manifest{
		ID: "test.wrapper", Revision: 1, Summary: "POSIX wrapper",
		Execution: &catalog.Execution{Kind: "cli", ExecutableRef: "tools/wrapper", CWD: ".", ArgvTemplate: []string{"{message}"}, TrustedForRun: true, TimeoutSec: 5},
		Arguments: map[string]catalog.Argument{"message": {Type: "string", Required: true}},
	}
	service, _, cleanup := newTestService(t, manifest)
	defer cleanup()
	script := "#!/bin/sh\nexport REPOPLANE_WRAPPER_ARG=\"$1\"\nexec \"$(dirname \"$0\")/repoplane-runner-test\"\n"
	path := filepath.Join(service.root.Resolved(), "tools", "wrapper")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REPOPLANE_RUNNER_HELPER", "1")
	assertWrapperOutput(t, service, "Unicode 공백 & literal")
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
	stream, err := service.Inspect(context.Background(), InspectRequest{RunID: executed.RunID, Action: "stdout", ResponseView: "bytes"})
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
	script := "#!/bin/sh\nif [ \"$REPOPLANE_RUNNER_SLEEP\" = 1 ]; then sleep 10; fi\nif [ -n \"$REPOPLANE_WRAPPER_ARG\" ]; then printf '%s' \"$REPOPLANE_WRAPPER_ARG\"; else printf '%s' 'runner stdout'; fi\nprintf '%s' 'runner stderr' >&2\nif [ \"$REPOPLANE_RUNNER_OUTPUT\" = 1 ]; then printf '%s' artifact > out.txt; fi\nif [ -n \"$REPOPLANE_RUNNER_EXIT\" ]; then exit \"$REPOPLANE_RUNNER_EXIT\"; fi\n"
	if err := os.WriteFile(destination, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}
