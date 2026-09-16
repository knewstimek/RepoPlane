package runner

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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

type fixedQualifications struct {
	refs    []string
	current bool
}

func (q fixedQualifications) ResolveCacheQualifications(context.Context, string, string, []string) ([]string, bool, error) {
	return append([]string(nil), q.refs...), q.current, nil
}

func TestVerifiedCacheMissThenReuseAndConflict(t *testing.T) {
	manifest := cacheTestManifest("verified")
	service, cleanup := newCacheTestService(t, manifest, fixedQualifications{refs: []string{"record:verification_current"}, current: true})
	defer cleanup()
	t.Setenv("REPOPLANE_RUNNER_HELPER", "1")
	t.Setenv("REPOPLANE_RUNNER_OUTPUT", "1")

	first, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: manifest.ID, CapabilityRevision: "1"})
	if err != nil || first.Plan.Cache.Status != "miss" || !first.Plan.Cache.Eligible {
		t.Fatalf("first prepare=%+v err=%v", first.Plan.Cache, err)
	}
	executed, err := service.Execute(context.Background(), ExecuteRequest{PlanID: first.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	completed := awaitTerminal(t, service, executed.RunID)
	if completed.Run["cache"].(map[string]any)["status"] != "observed" {
		t.Fatalf("cache observation missing: %+v", completed.Run["cache"])
	}
	want, err := os.ReadFile(filepath.Join(service.root.Resolved(), "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(service.root.Resolved(), "out.txt")); err != nil {
		t.Fatal(err)
	}
	second, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: manifest.ID, CapabilityRevision: "1"})
	if err != nil || second.Plan.Cache.Status != "hit" {
		t.Fatalf("second prepare=%+v err=%v", second.Plan.Cache, err)
	}
	reused, err := service.Execute(context.Background(), ExecuteRequest{PlanID: second.Plan.ID})
	if err != nil || reused.State != "reused" {
		t.Fatalf("reuse=%+v err=%v", reused, err)
	}
	got, err := os.ReadFile(filepath.Join(service.root.Resolved(), "out.txt"))
	if err != nil || string(got) != string(want) {
		t.Fatalf("materialized=%q err=%v want=%q", got, err, want)
	}

	third, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: manifest.ID, CapabilityRevision: "1"})
	if err != nil || third.Plan.Cache.Status != "hit" {
		t.Fatalf("third prepare=%+v err=%v", third.Plan.Cache, err)
	}
	if err := os.WriteFile(filepath.Join(service.root.Resolved(), "out.txt"), []byte("local change"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), ExecuteRequest{PlanID: third.Plan.ID}); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("conflicting output error=%v, want ErrPlanStale", err)
	}
	unchanged, _ := os.ReadFile(filepath.Join(service.root.Resolved(), "out.txt"))
	if string(unchanged) != "local change" {
		t.Fatalf("conflicting output was overwritten: %q", unchanged)
	}
}

func TestRuntimeCacheAuthorizationFailsClosed(t *testing.T) {
	manifest := cacheTestManifest("observe")
	service, cleanup := newCacheTestService(t, manifest, fixedQualifications{current: true})
	defer cleanup()
	service.SetCacheEnabled(func() bool { return false })
	prepared, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: manifest.ID, CapabilityRevision: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Plan.Cache.Eligible || prepared.Plan.Cache.Reason != "runtime_cache_not_approved" {
		t.Fatalf("cache decision=%+v", prepared.Plan.Cache)
	}
}

func TestCacheBypassAndQualificationFailureStillExecute(t *testing.T) {
	manifest := cacheTestManifest("verified")
	service, cleanup := newCacheTestService(t, manifest, fixedQualifications{current: false})
	defer cleanup()
	t.Setenv("REPOPLANE_RUNNER_HELPER", "1")
	t.Setenv("REPOPLANE_RUNNER_OUTPUT", "1")
	prepared, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: manifest.ID, CapabilityRevision: "1", CacheMode: "bypass"})
	if err != nil || prepared.Plan.Cache.Reason != "qualification_not_current" || prepared.Plan.Cache.Eligible {
		t.Fatalf("prepare cache=%+v err=%v", prepared.Plan.Cache, err)
	}
	executed, err := service.Execute(context.Background(), ExecuteRequest{PlanID: prepared.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	if terminal := awaitTerminal(t, service, executed.RunID); terminal.Run["state"] != "completed" {
		t.Fatalf("normal execution did not complete: %+v", terminal.Run)
	}
}

func TestCacheBypassSkipsHitAndCorruptBlobIsQuarantined(t *testing.T) {
	manifest := cacheTestManifest("verified")
	service, cleanup := newCacheTestService(t, manifest, fixedQualifications{refs: []string{"record:verification_current"}, current: true})
	defer cleanup()
	t.Setenv("REPOPLANE_RUNNER_HELPER", "1")
	t.Setenv("REPOPLANE_RUNNER_OUTPUT", "1")
	first, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: manifest.ID, CapabilityRevision: "1"})
	if err != nil {
		t.Fatal(err)
	}
	executed, err := service.Execute(context.Background(), ExecuteRequest{PlanID: first.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	_ = awaitTerminal(t, service, executed.RunID)

	bypassed, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: manifest.ID, CapabilityRevision: "1", CacheMode: "bypass"})
	if err != nil || bypassed.Plan.Cache.Status != "bypassed" {
		t.Fatalf("bypass=%+v err=%v", bypassed.Plan.Cache, err)
	}
	bypassRun, err := service.Execute(context.Background(), ExecuteRequest{PlanID: bypassed.Plan.ID})
	if err != nil || bypassRun.State != "running" {
		t.Fatalf("bypass execution=%+v err=%v", bypassRun, err)
	}
	_ = awaitTerminal(t, service, bypassRun.RunID)

	entry, err := service.cache.GetCacheEntry(context.Background(), service.projectID, service.workspaceID, first.Plan.Cache.Key)
	if err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(service.stateDir, "artifacts", "blobs", strings.TrimPrefix(entry.Outputs[0].ContentHash, "sha256:"))
	if err := os.WriteFile(blob, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: manifest.ID, CapabilityRevision: "1"})
	if err != nil || prepared.Plan.Cache.Status != "quarantined" || prepared.Plan.Cache.Reason != "artifact_unavailable" {
		t.Fatalf("corrupt prepare=%+v err=%v", prepared.Plan.Cache, err)
	}
}

func TestCacheKeyPreservesArgvOrderAndConfiguration(t *testing.T) {
	manifest := cacheTestManifest("observe")
	capability := catalog.Capability{Manifest: manifest, Revision: "1", ExecutionFingerprint: "sha256:manifest"}
	secret := []byte("01234567890123456789012345678901")
	base, err := makeCacheKey(secret, capability, "default", []string{"a", "b"}, map[string]string{"in": "sha256:x"}, map[string]string{"runtime": "sha256:y"})
	if err != nil {
		t.Fatal(err)
	}
	ordered, _ := makeCacheKey(secret, capability, "default", []string{"b", "a"}, map[string]string{"in": "sha256:x"}, map[string]string{"runtime": "sha256:y"})
	configured, _ := makeCacheKey(secret, capability, "release", []string{"a", "b"}, map[string]string{"in": "sha256:x"}, map[string]string{"runtime": "sha256:y"})
	inputChanged, _ := makeCacheKey(secret, capability, "default", []string{"a", "b"}, map[string]string{"in": "sha256:z"}, map[string]string{"runtime": "sha256:y"})
	runtimeChanged, _ := makeCacheKey(secret, capability, "default", []string{"a", "b"}, map[string]string{"in": "sha256:x"}, map[string]string{"runtime": "sha256:z"})
	if base == ordered || base == configured || base == inputChanged || base == runtimeChanged {
		t.Fatal("cache key lost argv, configuration, input, or runtime identity")
	}
}

func TestExecutableProbeOutputContributesToIdentity(t *testing.T) {
	service, _, cleanup := newTestService(t, testManifest())
	defer cleanup()
	declaration := catalog.PreflightCheck{ID: "runtime.version", Kind: "executable", Ref: helperRelativePath(), Requirement: "required", Argv: []string{"version"}}
	t.Setenv("REPOPLANE_WRAPPER_ARG", "runtime-one")
	first := service.runPreflightCheck(context.Background(), declaration)
	t.Setenv("REPOPLANE_WRAPPER_ARG", "runtime-two")
	second := service.runPreflightCheck(context.Background(), declaration)
	if first.Status != "passed" || second.Status != "passed" || first.Identity == second.Identity {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestGitPreflightUsesExecutionCWDForNestedRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	manifest := testManifest()
	manifest.Execution.CWD = "code"
	manifest.Execution.Preflight = []catalog.PreflightCheck{{ID: "git.repository", Kind: "git", Requirement: "required"}}
	service, _, cleanup := newTestService(t, manifest)
	defer cleanup()
	repository := filepath.Join(service.root.Resolved(), "code")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"-C", repository, "init"},
		{"-C", repository, "-c", "user.name=RepoPlane Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "baseline"},
	}
	for index, args := range commands {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatalf("git setup step %d: %v", index+1, err)
		}
	}
	prepared, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: manifest.ID, CapabilityRevision: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Plan.Ready {
		t.Fatalf("nested repository preflight blocked plan: %+v", prepared.Checks)
	}
	for _, check := range prepared.Checks {
		if check.ID == "git.repository" {
			if check.Status != "passed" || check.Identity == "" {
				t.Fatalf("git check=%+v", check)
			}
			return
		}
	}
	t.Fatal("git.repository check missing")
}

func TestIsolatedRootMaterializationReplacesWholeTree(t *testing.T) {
	service, _, cleanup := newTestService(t, testManifest())
	defer cleanup()
	source := filepath.Join(service.root.Resolved(), "cached.txt")
	if err := os.WriteFile(source, []byte("cached"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := hashFile(context.Background(), source, 1024)
	if err != nil || service.captureBlob(source, identity) != nil {
		t.Fatalf("capture identity=%q err=%v", identity, err)
	}
	root := filepath.Join(service.root.Resolved(), "dist")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "old.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	outputs := []store.CacheOutput{{Path: "dist/nested/out.txt", ContentHash: identity, Size: 6, ArtifactRef: "record:artifact_source"}}
	if err := service.materializeIsolatedRoot(context.Background(), "dist", outputs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "old.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old isolated output remains: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "nested", "out.txt"))
	if err != nil || string(got) != "cached" {
		t.Fatalf("materialized=%q err=%v", got, err)
	}
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
	if prepared.Plan.InputCount != 1 || len(prepared.Plan.InputHashes) != 0 {
		t.Fatalf("prepare must summarize retained input hashes: %+v", prepared.Plan)
	}
	executed, err := service.Execute(context.Background(), ExecuteRequest{PlanID: prepared.Plan.ID})
	if err != nil || executed.State != "running" {
		t.Fatalf("execute=%+v err=%v", executed, err)
	}
	result := awaitTerminal(t, service, executed.RunID)
	if result.Run["state"] != "completed" {
		t.Fatalf("run did not complete: %+v", result.Run)
	}
	if _, ok := result.Run["input_hashes"]; ok {
		t.Fatal("status repeated the full run receipt")
	}
	detailRef, err := service.Inspect(context.Background(), InspectRequest{RunID: executed.RunID, Action: "detail", ByteLimit: 1})
	if err != nil || detailRef.Run["receipt_ref"] != "record:"+executed.RunID || detailRef.Run["input_hashes"] != nil {
		t.Fatalf("detail reference exposed content: %+v err=%v", detailRef.Run, err)
	}
	detail, err := service.Inspect(context.Background(), InspectRequest{RunID: executed.RunID, Action: "detail", ResponseView: "bytes", ByteLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	hashes, ok := detail.Run["input_hashes"].(map[string]any)
	if !ok || len(hashes) != 1 {
		t.Fatalf("detail did not retain full input evidence: %+v err=%v", detail.Run, err)
	}
	stream, err := service.Inspect(context.Background(), InspectRequest{RunID: executed.RunID, Action: "stdout"})
	if err != nil {
		t.Fatal(err)
	}
	if stream.Stream.BytesBase64 != "" || stream.Stream.FileRef != "state:runs/"+executed.RunID+"/stdout.log" || stream.Stream.SizeBytes == 0 {
		t.Fatalf("stdout reference exposed content: %+v", stream.Stream)
	}
	stream, err = service.Inspect(context.Background(), InspectRequest{RunID: executed.RunID, Action: "stdout", ResponseView: "bytes"})
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
	if artifact.Stream.BytesBase64 != "" || !strings.HasPrefix(artifact.Stream.FileRef, "state:artifacts/blobs/") || artifact.Stream.SizeBytes == 0 {
		t.Fatalf("artifact reference exposed content: %+v", artifact.Stream)
	}
	artifact, err = service.Inspect(context.Background(), InspectRequest{RunID: executed.RunID, Action: "artifact", ArtifactRef: artifactRef, ResponseView: "bytes"})
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

func TestHostRefAddsOptionalFactCheckAndConflictWarning(t *testing.T) {
	manifest := testManifest()
	manifest.Arguments = map[string]catalog.Argument{"target": {Type: "host_ref", Required: true}}
	manifest.Execution.ArgvTemplate = []string{"{target}"}
	service, records, cleanup := newTestService(t, manifest)
	defer cleanup()
	now := time.Now().UTC()
	create := func(id, role, operatingSystem string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{
			"memo_kind": "host_fact", "scope": "operations/hosts", "configuration": "", "content": "",
			"host":                   map[string]any{"alias": "edge-a", "role": role, "os": operatingSystem, "tier": "production", "services": []string{"proxy"}, "paths": []string{"/srv/proxy"}, "confirmed_at": now.Format(time.RFC3339)},
			"invalidation_condition": "the host is rebuilt",
		})
		_, err := records.CreateMemo(context.Background(), store.RecordCreate{Record: store.Record{
			ID: id, Kind: "memo", SchemaVersion: "memo.v2", ProjectID: service.projectID, WorkspaceID: service.workspaceID,
			Revision: 1, Source: "user_asserted", WriterClass: "intention", Validity: "current", CreatedAt: now, UpdatedAt: now, Payload: payload, EvidenceRefs: []string{},
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	missing, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: manifest.ID, CapabilityRevision: "1", Arguments: map[string]any{"target": "HOST-B"}})
	if err != nil {
		t.Fatal(err)
	}
	assertHostCheck(t, missing, "unknown", "")
	if !missing.Plan.Ready || len(missing.Warnings) != 1 || missing.Warnings[0].Code != "host_fact_missing" {
		t.Fatalf("missing optional host fact=%+v", missing)
	}
	create("memo_host_one", "gateway", "linux")
	prepared, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: manifest.ID, CapabilityRevision: "1", Arguments: map[string]any{"target": "EDGE-A"}})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Plan.Ready || prepared.Plan.Argv[0] != "edge-a" {
		t.Fatalf("host_ref normalization=%+v", prepared.Plan)
	}
	assertHostCheck(t, prepared, "passed", "record:memo_host_one")
	create("memo_host_two", "database", "windows")
	conflicting, err := service.Prepare(context.Background(), PrepareRequest{CapabilityID: manifest.ID, CapabilityRevision: "1", Arguments: map[string]any{"target": "edge-a"}})
	if err != nil {
		t.Fatal(err)
	}
	assertHostCheck(t, conflicting, "unknown", "")
	if !conflicting.Plan.Ready || len(conflicting.Warnings) != 1 || conflicting.Warnings[0].Code != "host_fact_conflict" {
		t.Fatalf("host conflict=%+v", conflicting)
	}
}

func assertHostCheck(t *testing.T, response PrepareResponse, status, identity string) {
	t.Helper()
	for _, check := range response.Checks {
		if check.ID == "host.target" {
			if check.Status != status || check.Identity != identity || check.Requirement != "informational" {
				t.Fatalf("host check=%+v", check)
			}
			return
		}
	}
	t.Fatal("host fact check missing")
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
	service := NewService(root, fixedResolver{capability: capability}, records, nil, nil, stateDirectory, nil)
	return service, records, func() {
		_ = service.Close()
		_ = records.Close()
	}
}

func cacheTestManifest(policy string) catalog.Manifest {
	return catalog.Manifest{
		ID: "test.cache", Revision: 1, Summary: "pure cache fixture",
		Execution: &catalog.Execution{Kind: "cli", ExecutableRef: helperRelativePath(), CWD: ".", TrustedForRun: true, TimeoutSec: 5, ArtifactMode: "capture", Preflight: []catalog.PreflightCheck{{ID: "runtime.version", Kind: "executable", Ref: "go", Requirement: "required", Argv: []string{"version"}}}},
		Inputs:    []string{"input.txt"}, Outputs: []string{"out.txt"}, CachePolicy: policy,
		Cache: &catalog.Cache{ContractRevision: 1, OutputContract: "test-output.v1", KeyChecks: []string{"runtime.version"}, QualificationChecks: []string{"cache.test.differential"}, RestorePolicy: "missing_or_matching", Assumptions: catalog.CacheAssumptions{InputsComplete: true, OutputsComplete: true, ExternalState: "none", Nondeterminism: "none", SideEffects: "declared_outputs_only"}},
	}
}

func newCacheTestService(t *testing.T, manifest catalog.Manifest, qualifications qualificationResolver) (*Service, func()) {
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
	cache, err := storesqlite.Open(context.Background(), filepath.Join(stateDirectory, "repoplane.db"))
	if err != nil {
		t.Fatal(err)
	}
	capability := catalog.Capability{Manifest: manifest, Revision: "1", SourceRef: "source:test", ExecutionFingerprint: "sha256:manifest", GenerationID: "test"}
	service := NewService(root, fixedResolver{capability: capability}, records, cache, qualifications, stateDirectory, []byte("01234567890123456789012345678901"))
	return service, func() {
		_ = service.Close()
		_ = records.Close()
		_ = cache.Close()
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
