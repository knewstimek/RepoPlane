package runner

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"repoplane/internal/catalog"
	"repoplane/internal/store"
	storesqlite "repoplane/internal/store/sqlite"
	"repoplane/internal/workspace"
)

type fixedResolver struct{ capability catalog.Capability }

func (resolver fixedResolver) ResolveCapability(context.Context, string) (catalog.Capability, error) {
	return resolver.capability, nil
}

func TestPrepareExecuteInspectAndCapture(t *testing.T) {
	service, records, cleanup := newTestService(t, catalog.Manifest{
		ID: "test.run", Revision: 1, Summary: "runner integration",
		Execution: &catalog.Execution{Kind: "cli", ExecutableRef: helperRelativePath(), CWD: ".", TrustedForRun: true, TimeoutSec: 5, ArtifactMode: "capture",
			Preflight: []catalog.PreflightCheck{{ID: "optional.env", Kind: "environment", Ref: "REPOPLANE_OPTIONAL_MISSING", Requirement: "recommended"}}},
		Inputs: []string{"input.txt"}, Outputs: []string{"out.txt"},
	})
	defer cleanup()
	t.Setenv("REPOPLANE_RUNNER_HELPER", "1")
	t.Setenv("REPOPLANE_RUNNER_OUTPUT", "1")
	prepared, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: "test.run", CapabilityRevision: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Plan.Ready || prepared.Status != "ok" || len(prepared.Warnings) != 1 {
		t.Fatalf("unexpected prepare response: %+v", prepared)
	}
	executed, err := service.Execute(context.Background(), ExecuteRequest{PlanID: prepared.Plan.ID})
	if err != nil || executed.State != "running" {
		t.Fatalf("execute=%+v err=%v", executed, err)
	}
	result := awaitTerminal(t, service, executed.RunID)
	if result.Run["state"] != "completed" {
		t.Fatalf("run did not complete: %+v", result.Run)
	}
	stream, err := service.Inspect(context.Background(), InspectRequest{RunID: executed.RunID, Action: "stdout"})
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(stream.Stream.BytesBase64)
	if strings.TrimSpace(string(decoded)) != "runner stdout" || !stream.Stream.EOF {
		t.Fatalf("unexpected stream: %+v %q", stream.Stream, decoded)
	}
	page, err := records.QueryRecords(context.Background(), storeQuery(service, "artifact"))
	if err != nil || len(page.Records) != 1 || !strings.Contains(string(page.Records[0].Payload), `"storage":"stored"`) {
		t.Fatalf("artifact records=%+v err=%v", page.Records, err)
	}
	artifactRef := "record:" + page.Records[0].ID
	artifact, err := service.Inspect(context.Background(), InspectRequest{RunID: executed.RunID, Action: "artifact", ArtifactRef: artifactRef})
	if err != nil {
		t.Fatal(err)
	}
	artifactBytes, _ := base64.StdEncoding.DecodeString(artifact.Stream.BytesBase64)
	if strings.TrimSpace(string(artifactBytes)) != "artifact" || artifact.Stream.Availability != "stored" {
		t.Fatalf("unexpected artifact stream: %+v %q", artifact.Stream, artifactBytes)
	}
	var observed artifactPayload
	if err := json.Unmarshal(page.Records[0].Payload, &observed); err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(service.stateDir, "artifacts", "blobs", strings.TrimPrefix(observed.ContentHash, "sha256:"))
	if err := os.WriteFile(blob, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err = service.Inspect(context.Background(), InspectRequest{RunID: executed.RunID, Action: "artifact", ArtifactRef: artifactRef})
	if err != nil || artifact.Stream.Availability != "corrupt" || len(artifact.Warnings) != 1 {
		t.Fatalf("corruption was not reported: response=%+v err=%v", artifact, err)
	}
	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}
	artifact, err = service.Inspect(context.Background(), InspectRequest{RunID: executed.RunID, Action: "artifact", ArtifactRef: artifactRef})
	if err != nil || artifact.Stream.Availability != "missing" || len(artifact.Warnings) != 1 {
		t.Fatalf("missing blob was not reported: response=%+v err=%v", artifact, err)
	}
}

func TestExecuteRejectsChangedDeclaredInputButNotUnrelatedFile(t *testing.T) {
	service, _, cleanup := newTestService(t, testManifest())
	defer cleanup()
	t.Setenv("REPOPLANE_RUNNER_HELPER", "1")
	prepared, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: "test.run", CapabilityRevision: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.root.Resolved(), "unrelated.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	executed, err := service.Execute(context.Background(), ExecuteRequest{PlanID: prepared.Plan.ID})
	if err != nil {
		t.Fatalf("unrelated worktree file invalidated plan: %v", err)
	}
	_ = awaitTerminal(t, service, executed.RunID)

	prepared, err = service.Prepare(context.Background(), PrepareRequest{CapabilityID: "test.run", CapabilityRevision: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.root.Resolved(), "input.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), ExecuteRequest{PlanID: prepared.Plan.ID}); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("changed declared input error=%v", err)
	}
}

func TestRequiredPreflightBlocksWhileRecommendedWarns(t *testing.T) {
	manifest := testManifest()
	manifest.Execution.Preflight = []catalog.PreflightCheck{{ID: "required.env", Kind: "environment", Ref: "REPOPLANE_REQUIRED_MISSING", Requirement: "required"}}
	service, _, cleanup := newTestService(t, manifest)
	defer cleanup()
	prepared, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: "test.run", CapabilityRevision: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Plan.Ready || prepared.Status != "partial" {
		t.Fatalf("required missing check did not block: %+v", prepared)
	}
	if _, err := service.Execute(context.Background(), ExecuteRequest{PlanID: prepared.Plan.ID}); !errors.Is(err, ErrNotExecutable) {
		t.Fatalf("blocked plan execution error=%v", err)
	}
}

func TestRunTimeoutIsRecorded(t *testing.T) {
	manifest := testManifest()
	manifest.Execution.TimeoutSec = 1
	service, _, cleanup := newTestService(t, manifest)
	defer cleanup()
	t.Setenv("REPOPLANE_RUNNER_HELPER", "1")
	t.Setenv("REPOPLANE_RUNNER_SLEEP", "1")
	prepared, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: "test.run", CapabilityRevision: "1"})
	if err != nil {
		t.Fatal(err)
	}
	executed, err := service.Execute(context.Background(), ExecuteRequest{PlanID: prepared.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	result := awaitTerminal(t, service, executed.RunID)
	if result.Run["state"] != "timed_out" || result.Run["termination_reason"] != "timeout" {
		t.Fatalf("timeout not recorded: %+v", result.Run)
	}
}

func TestFailureExitCodeIsRecorded(t *testing.T) {
	service, _, cleanup := newTestService(t, testManifest())
	defer cleanup()
	t.Setenv("REPOPLANE_RUNNER_EXIT", "7")
	prepared, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: "test.run", CapabilityRevision: "1"})
	if err != nil {
		t.Fatal(err)
	}
	executed, err := service.Execute(context.Background(), ExecuteRequest{PlanID: prepared.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	result := awaitTerminal(t, service, executed.RunID)
	if result.Run["state"] != "failed" || result.Run["exit_code"] != json.Number("7") {
		t.Fatalf("failure exit was not recorded: %+v", result.Run)
	}
}

func TestCancelAndDuplicateExecuteAreRecorded(t *testing.T) {
	service, _, cleanup := newTestService(t, testManifest())
	defer cleanup()
	t.Setenv("REPOPLANE_RUNNER_SLEEP", "1")
	prepared, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: "test.run", CapabilityRevision: "1"})
	if err != nil {
		t.Fatal(err)
	}
	executed, err := service.Execute(context.Background(), ExecuteRequest{PlanID: prepared.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), ExecuteRequest{PlanID: prepared.Plan.ID}); !errors.Is(err, ErrRunState) {
		t.Fatalf("duplicate execute error=%v", err)
	}
	if _, err := service.Inspect(context.Background(), InspectRequest{RunID: executed.RunID, Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	result := awaitTerminal(t, service, executed.RunID)
	if result.Run["state"] != "cancelled" || result.Run["termination_reason"] != "cancelled" {
		t.Fatalf("cancel was not recorded: %+v", result.Run)
	}
}

func TestStreamAndArtifactLimitsRemainExplicit(t *testing.T) {
	manifest := testManifest()
	manifest.Execution.ArtifactMode = "capture"
	manifest.Outputs = []string{"out.txt"}
	service, records, cleanup := newTestService(t, manifest)
	defer cleanup()
	service.streamByteLimit = 4
	service.artifactByteLimit = 4
	t.Setenv("REPOPLANE_RUNNER_OUTPUT", "1")
	prepared, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: "test.run", CapabilityRevision: "1"})
	if err != nil {
		t.Fatal(err)
	}
	executed, err := service.Execute(context.Background(), ExecuteRequest{PlanID: prepared.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	result := awaitTerminal(t, service, executed.RunID)
	if result.Run["stdout_truncated"] != true || result.Run["observation_partial"] != true {
		t.Fatalf("limits were not recorded: %+v", result.Run)
	}
	page, err := records.QueryRecords(context.Background(), storeQuery(service, "artifact"))
	if err != nil || len(page.Records) != 1 || !strings.Contains(string(page.Records[0].Payload), `"storage":"not_stored_limit"`) {
		t.Fatalf("artifact limit state missing: records=%+v err=%v", page.Records, err)
	}
}

func TestRecoverMarksRunningReceiptInterrupted(t *testing.T) {
	service, _, cleanup := newTestService(t, testManifest())
	defer cleanup()
	prepared, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: "test.run", CapabilityRevision: "1"})
	if err != nil {
		t.Fatal(err)
	}
	record, payload, err := service.loadRun(context.Background(), prepared.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload.State = "running"
	now := time.Now().UTC()
	payload.StartedAt = &now
	if _, err := service.updateRun(context.Background(), record, payload, "current"); err != nil {
		t.Fatal(err)
	}
	if err := service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, recovered, err := service.loadRun(context.Background(), prepared.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != "interrupted" || recovered.TerminationReason != "server_restart" || !recovered.ObservationPartial {
		t.Fatalf("unexpected recovered receipt: %+v", recovered)
	}
}

func TestStreamRetentionKeepsRecentAndReferencedRuns(t *testing.T) {
	service, records, cleanup := newTestService(t, testManifest())
	defer cleanup()
	service.retentionRunCount = 1
	service.retentionAge = 24 * time.Hour
	now := time.Now().UTC()
	ids := []string{"run_00000000000000000000000000000001", "run_00000000000000000000000000000002", "run_00000000000000000000000000000003"}
	artifactIDs := []string{"artifact_00000000000000000000000000000001", "artifact_00000000000000000000000000000002", "artifact_00000000000000000000000000000003"}
	blobDirectory := filepath.Join(service.stateDir, "artifacts", "blobs")
	if err := os.MkdirAll(blobDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	for index, id := range ids {
		observed := now.Add(time.Duration(index-3) * 24 * time.Hour)
		payload, _ := json.Marshal(runPayload{State: "completed", StreamsRetained: true})
		_, err := records.CreateObservation(context.Background(), store.RecordCreate{Record: store.Record{
			ID: id, Kind: "run", SchemaVersion: "run-receipt.v1", ProjectID: service.projectID, WorkspaceID: service.workspaceID,
			Revision: 1, Source: "observed", WriterClass: "server", Validity: "current", CreatedAt: observed, UpdatedAt: observed,
			Payload: payload, EvidenceRefs: []string{},
		}})
		if err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(service.stateDir, "runs", id)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "stdout.log"), []byte("stream"), 0o600); err != nil {
			t.Fatal(err)
		}
		content := []byte(id)
		sum := sha256.Sum256(content)
		identity := "sha256:" + hex.EncodeToString(sum[:])
		if err := os.WriteFile(filepath.Join(blobDirectory, strings.TrimPrefix(identity, "sha256:")), content, 0o600); err != nil {
			t.Fatal(err)
		}
		artifactJSON, _ := json.Marshal(artifactPayload{RunRef: "record:" + id, Path: "out.txt", ContentHash: identity, Size: int64(len(content)), Storage: "stored", Basis: "observed", WriterAttribution: "unknown"})
		_, err = records.CreateObservation(context.Background(), store.RecordCreate{Record: store.Record{
			ID: artifactIDs[index], Kind: "artifact", SchemaVersion: "artifact-record.v1", ProjectID: service.projectID, WorkspaceID: service.workspaceID,
			Revision: 1, Source: "observed", WriterClass: "server", Validity: "current", CreatedAt: observed, UpdatedAt: observed,
			Payload: artifactJSON, EvidenceRefs: []string{"record:" + id},
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	checkpointPayload := json.RawMessage(`{"run_refs":["record:run_00000000000000000000000000000001"]}`)
	_, err := records.CreateCheckpoint(context.Background(), store.RecordCreate{Record: store.Record{
		ID: "checkpoint_00000000000000000000000000000001", Kind: "checkpoint", SchemaVersion: "checkpoint.v1",
		ProjectID: service.projectID, WorkspaceID: service.workspaceID, Revision: 1, Source: "user_asserted", WriterClass: "intention",
		Validity: "current", CreatedAt: now, UpdatedAt: now, Payload: checkpointPayload, EvidenceRefs: []string{"record:" + ids[0]},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.cleanupExpiredRuns(context.Background(), now, 64); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(service.stateDir, "runs", ids[0])); err != nil {
		t.Fatalf("referenced run stream was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(service.stateDir, "runs", ids[1])); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired unreferenced run stream remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(service.stateDir, "runs", ids[2])); err != nil {
		t.Fatalf("recent run stream was removed: %v", err)
	}
	for _, index := range []int{0, 2} {
		record, err := records.GetRecord(context.Background(), service.projectID, service.workspaceID, artifactIDs[index])
		if err != nil || strings.Contains(string(record.Payload), `"storage":"deleted"`) {
			t.Fatalf("protected/recent artifact was deleted: index=%d record=%+v err=%v", index, record, err)
		}
	}
	expiredArtifact, err := records.GetRecord(context.Background(), service.projectID, service.workspaceID, artifactIDs[1])
	if err != nil || !strings.Contains(string(expiredArtifact.Payload), `"storage":"deleted"`) {
		t.Fatalf("expired artifact was not marked deleted: record=%+v err=%v", expiredArtifact, err)
	}
}

func testManifest() catalog.Manifest {
	return catalog.Manifest{ID: "test.run", Revision: 1, Summary: "test runner", Execution: &catalog.Execution{
		Kind: "cli", ExecutableRef: helperRelativePath(), CWD: ".", TrustedForRun: true, TimeoutSec: 5,
	}, Inputs: []string{"input.txt"}}
}

func helperRelativePath() string {
	if runtime.GOOS == "windows" {
		return "tools/repoplane-runner-test.cmd"
	}
	return "tools/repoplane-runner-test"
}

func newTestService(t *testing.T, manifest catalog.Manifest) (*Service, *storesqlite.RecordRepository, func()) {
	t.Helper()
	workspaceDirectory := t.TempDir()
	stateDirectory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspaceDirectory, "tools"), 0o700); err != nil {
		t.Fatal(err)
	}
	writePlatformHelper(t, filepath.Join(workspaceDirectory, filepath.FromSlash(helperRelativePath())))
	if err := os.WriteFile(filepath.Join(workspaceDirectory, "input.txt"), []byte("input"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := workspace.Open(workspaceDirectory)
	if err != nil {
		t.Fatal(err)
	}
	records, err := storesqlite.OpenRecords(context.Background(), filepath.Join(stateDirectory, "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	capability := catalog.Capability{Manifest: manifest, Revision: "1", SourceRef: "source:test", ExecutionFingerprint: "sha256:manifest", GenerationID: "test"}
	service := NewService(root, fixedResolver{capability: capability}, records, stateDirectory)
	return service, records, func() {
		_ = service.Close()
		_ = records.Close()
	}
}

func awaitTerminal(t *testing.T, service *Service, runID string) InspectResponse {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		result, err := service.Inspect(context.Background(), InspectRequest{RunID: runID})
		if err != nil {
			t.Fatal(err)
		}
		if state, _ := result.Run["state"].(string); state != "running" {
			return result
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("run did not reach terminal state")
	return InspectResponse{}
}

func storeQuery(service *Service, kind string) store.RecordQuery {
	return store.RecordQuery{ProjectID: service.projectID, WorkspaceID: service.workspaceID, Kind: kind, Limit: 100}
}
