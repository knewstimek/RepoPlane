package runner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"repoplane/internal/contracts"
	"repoplane/internal/store"
)

type boundedFileWriter struct {
	mu        sync.Mutex
	file      *os.File
	limit     uint64
	written   uint64
	truncated bool
}

func (w *boundedFileWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	accepted := len(data)
	remaining := uint64(0)
	if w.written < w.limit {
		remaining = w.limit - w.written
	}
	toWrite := data
	if uint64(len(toWrite)) > remaining {
		toWrite = toWrite[:remaining]
		w.truncated = true
	}
	if len(toWrite) > 0 {
		count, err := w.file.Write(toWrite)
		w.written += uint64(count)
		if err != nil {
			return count, err
		}
	}
	return accepted, nil
}

func (w *boundedFileWriter) snapshot() (uint64, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.written, w.truncated
}

func (s *Service) Execute(ctx context.Context, request ExecuteRequest) (ExecuteResponse, error) {
	if !validRunID(request.PlanID) {
		return ExecuteResponse{}, errors.New("valid plan_id is required")
	}
	limits, err := contracts.NormalizeLimits(contracts.LimitRequest{TimeLimitMS: request.TimeLimitMS})
	if err != nil {
		return ExecuteResponse{}, err
	}
	startCtx, cancelStart := context.WithTimeout(ctx, limits.TimeLimit)
	defer cancelStart()
	record, payload, err := s.loadRun(startCtx, request.PlanID)
	if err != nil {
		return ExecuteResponse{}, err
	}
	if record.Revision != 1 || payload.State != "prepared" {
		return ExecuteResponse{}, ErrRunState
	}
	if !payload.Ready {
		return ExecuteResponse{}, ErrNotExecutable
	}
	capability, err := s.catalog.ResolveCapability(startCtx, payload.CapabilityID)
	if err != nil {
		return ExecuteResponse{}, err
	}
	if capability.Revision != payload.CapabilityRevision || capability.ExecutionFingerprint != payload.ExecutionFingerprint || capability.Manifest.Execution == nil || !capability.Manifest.Execution.TrustedForRun {
		return ExecuteResponse{}, ErrPlanStale
	}
	executable, identity, err := s.resolveExecutable(startCtx, payload.ExecutableRef)
	if err != nil || identity != payload.ExecutableIdentity {
		return ExecuteResponse{}, ErrPlanStale
	}
	inputs, err := s.snapshotPatterns(startCtx, capability.Manifest.Inputs)
	if err != nil || !reflect.DeepEqual(inputs, payload.InputHashes) {
		return ExecuteResponse{}, ErrPlanStale
	}
	cwd, relativeCWD, err := s.resolveCWD(capability.Manifest.Execution.CWD)
	if err != nil || relativeCWD != payload.CWD {
		return ExecuteResponse{}, ErrPlanStale
	}
	if payload.Cache.Status == "hit" {
		return s.executeCacheHit(startCtx, record, &payload, capability)
	}
	runDirectory := filepath.Join(s.stateDir, "runs", request.PlanID)
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		return ExecuteResponse{}, fmt.Errorf("create run directory: %w", err)
	}
	stdoutFile, err := os.OpenFile(filepath.Join(runDirectory, "stdout.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ExecuteResponse{}, fmt.Errorf("create stdout capture: %w", err)
	}
	stderrFile, err := os.OpenFile(filepath.Join(runDirectory, "stderr.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = stdoutFile.Close()
		return ExecuteResponse{}, fmt.Errorf("create stderr capture: %w", err)
	}
	stdout := &boundedFileWriter{file: stdoutFile, limit: s.streamByteLimit}
	stderr := &boundedFileWriter{file: stderrFile, limit: s.streamByteLimit}
	runContext, cancelRun := context.WithTimeout(context.Background(), time.Duration(payload.TimeoutSec)*time.Second)
	command, err := newProcessCommand(context.Background(), executable, payload.Argv)
	if err != nil {
		cancelRun()
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return ExecuteResponse{}, err
	}
	command.Dir = cwd
	command.Stdout = stdout
	command.Stderr = stderr
	configureProcess(command)
	if err := command.Start(); err != nil {
		cancelRun()
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		payload.State = "failed"
		finished := s.now().UTC()
		payload.FinishedAt = &finished
		payload.TerminationReason = "start_failed"
		_, _ = s.updateRun(context.Background(), record, payload, "current")
		return ExecuteResponse{}, fmt.Errorf("start registered capability: %w", err)
	}
	started := s.now().UTC()
	payload.State = "running"
	payload.StartedAt = &started
	payload.StreamsRetained = true
	runningRecord, err := s.updateRun(startCtx, record, payload, "current")
	if err != nil {
		cancelRun()
		_ = terminateProcessTree(command.Process)
		_ = command.Wait()
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return ExecuteResponse{}, err
	}
	running := &runningProcess{cancel: cancelRun, done: make(chan struct{})}
	s.mu.Lock()
	if _, exists := s.running[request.PlanID]; exists {
		s.mu.Unlock()
		cancelRun()
		_ = terminateProcessTree(command.Process)
		return ExecuteResponse{}, ErrRunState
	}
	s.running[request.PlanID] = running
	s.mu.Unlock()
	go s.waitForRun(runContext, request.PlanID, runningRecord, payload, command, stdout, stderr, stdoutFile, stderrFile, running)
	return ExecuteResponse{Status: contracts.StatusOK, RunID: request.PlanID, State: "running", Warnings: contracts.EmptyWarnings()}, nil
}

func (s *Service) waitForRun(runContext context.Context, id string, record store.Record, payload runPayload, command *exec.Cmd, stdout, stderr *boundedFileWriter, stdoutFile, stderrFile *os.File, running *runningProcess) {
	defer running.cancel()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	var waitErr error
	termination := "completed"
	select {
	case waitErr = <-wait:
	case <-runContext.Done():
		termination = "cancelled"
		if errors.Is(runContext.Err(), context.DeadlineExceeded) {
			termination = "timeout"
		}
		_ = terminateProcessTree(command.Process)
		waitErr = <-wait
	}
	_ = stdoutFile.Sync()
	_ = stderrFile.Sync()
	_ = stdoutFile.Close()
	_ = stderrFile.Close()
	stdoutBytes, stdoutTruncated := stdout.snapshot()
	stderrBytes, stderrTruncated := stderr.snapshot()
	finished := s.now().UTC()
	payload.FinishedAt = &finished
	payload.StdoutBytes, payload.StderrBytes = stdoutBytes, stderrBytes
	payload.StdoutTruncated, payload.StderrTruncated = stdoutTruncated, stderrTruncated
	payload.TerminationReason = termination
	exitCode := 0
	if waitErr != nil {
		exitCode = -1
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			exitCode = exitError.ExitCode()
		}
	}
	payload.ExitCode = &exitCode
	switch {
	case termination == "timeout":
		payload.State = "timed_out"
	case termination == "cancelled":
		payload.State = "cancelled"
	case waitErr != nil:
		payload.State = "failed"
	default:
		payload.State = "completed"
	}
	artifactRefs, outputsAfter, partial := s.observeArtifacts(context.Background(), id, payload)
	payload.ArtifactRefs = artifactRefs
	payload.OutputsAfter = outputsAfter
	payload.ObservationPartial = partial
	s.publishCacheObservation(context.Background(), id, &payload)
	_, _ = s.updateRun(context.Background(), record, payload, "current")
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
	close(running.done)
}

func (s *Service) Inspect(ctx context.Context, request InspectRequest) (InspectResponse, error) {
	if !validRunID(request.RunID) {
		return InspectResponse{}, errors.New("valid run_id is required")
	}
	if request.Action == "" {
		request.Action = "status"
	}
	limits, err := contracts.NormalizeLimits(contracts.LimitRequest{ByteLimit: request.ByteLimit, TimeLimitMS: request.TimeLimitMS})
	if err != nil {
		return InspectResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, limits.TimeLimit)
	defer cancel()
	if request.Action == "cancel" {
		s.mu.Lock()
		running := s.running[request.RunID]
		s.mu.Unlock()
		if running == nil {
			return InspectResponse{}, ErrRunState
		}
		running.cancel()
	}
	_, payload, err := s.loadRun(ctx, request.RunID)
	if err != nil {
		return InspectResponse{}, err
	}
	var view any = payload
	if request.Action != "detail" {
		view = map[string]any{
			"run_id": request.RunID, "state": payload.State, "capability_id": payload.CapabilityID,
			"ready": payload.Ready, "started_at": payload.StartedAt, "finished_at": payload.FinishedAt,
			"exit_code": payload.ExitCode, "termination_reason": payload.TerminationReason,
			"stdout_bytes": payload.StdoutBytes, "stderr_bytes": payload.StderrBytes,
			"stdout_truncated": payload.StdoutTruncated, "stderr_truncated": payload.StderrTruncated,
			"streams_retained": payload.StreamsRetained, "observation_partial": payload.ObservationPartial,
			"artifact_refs": payload.ArtifactRefs,
			"cache":         map[string]any{"status": payload.Cache.Status, "reason": payload.Cache.Reason},
		}
	}
	encoded, _ := json.Marshal(view)
	var run map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	_ = decoder.Decode(&run)
	response := InspectResponse{Status: contracts.StatusOK, Run: run, Warnings: contracts.EmptyWarnings()}
	switch request.Action {
	case "status", "detail", "cancel":
		return response, nil
	case "stdout", "stderr":
		if !payload.StreamsRetained {
			response.Stream = &StreamResult{Ref: "stream:" + request.RunID + ":" + request.Action, Offset: request.Offset, NextOffset: request.Offset, EOF: true, Availability: "expired"}
			response.Warnings = append(response.Warnings, contracts.Warning{Code: "stream_expired", Message: "captured stream is no longer retained"})
			return response, nil
		}
		stream, err := s.readStream(request.RunID, request.Action, request.Offset, limits.ByteLimit)
		if err != nil {
			return InspectResponse{}, err
		}
		response.Stream = &stream
		return response, nil
	case "artifact":
		stream, warning, err := s.readArtifact(ctx, request.RunID, request.ArtifactRef, request.Offset, limits.ByteLimit)
		if err != nil {
			return InspectResponse{}, err
		}
		response.Stream = &stream
		if warning.Code != "" {
			response.Warnings = append(response.Warnings, warning)
		}
		return response, nil
	default:
		return InspectResponse{}, fmt.Errorf("unsupported inspect action %q", request.Action)
	}
}

func (s *Service) readArtifact(ctx context.Context, runID, ref string, offset, limit uint64) (StreamResult, contracts.Warning, error) {
	id := strings.TrimPrefix(ref, "record:")
	if !validArtifactID(id) {
		return StreamResult{}, contracts.Warning{}, errors.New("valid artifact_ref is required")
	}
	record, err := s.reader.GetRecord(ctx, s.projectID, s.workspaceID, id)
	if err != nil || record.Kind != "artifact" || record.WriterClass != "server" {
		return StreamResult{}, contracts.Warning{}, store.ErrNotFound
	}
	var payload artifactPayload
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		return StreamResult{}, contracts.Warning{}, err
	}
	if payload.RunRef != "record:"+runID {
		return StreamResult{}, contracts.Warning{}, store.ErrNotFound
	}
	result := StreamResult{Ref: "record:" + id, Offset: offset, NextOffset: offset, EOF: true, Availability: payload.Storage}
	if payload.Storage != "stored" {
		return result, contracts.Warning{Code: "artifact_not_stored", Message: "artifact bytes were not retained"}, nil
	}
	if !strings.HasPrefix(payload.ContentHash, "sha256:") || len(payload.ContentHash) != len("sha256:")+64 {
		result.Availability = "corrupt"
		return result, contracts.Warning{Code: "artifact_corrupt", Message: "artifact identity is invalid"}, nil
	}
	path := filepath.Join(s.stateDir, "artifacts", "blobs", strings.TrimPrefix(payload.ContentHash, "sha256:"))
	identity, err := hashFile(ctx, path, int64(s.artifactByteLimit))
	if errors.Is(err, os.ErrNotExist) {
		result.Availability = "missing"
		return result, contracts.Warning{Code: "artifact_missing", Message: "retained artifact blob is missing"}, nil
	}
	if err != nil || identity != payload.ContentHash {
		result.Availability = "corrupt"
		return result, contracts.Warning{Code: "artifact_corrupt", Message: "retained artifact blob failed identity verification"}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return StreamResult{}, contracts.Warning{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return StreamResult{}, contracts.Warning{}, err
	}
	if offset > uint64(info.Size()) {
		return StreamResult{}, contracts.Warning{}, errors.New("artifact offset exceeds observed size")
	}
	if _, err := file.Seek(int64(offset), io.SeekStart); err != nil {
		return StreamResult{}, contracts.Warning{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)))
	if err != nil {
		return StreamResult{}, contracts.Warning{}, err
	}
	next := offset + uint64(len(data))
	result.NextOffset, result.BytesBase64 = next, base64.StdEncoding.EncodeToString(data)
	result.EOF, result.Truncated, result.Availability = next >= uint64(info.Size()), next < uint64(info.Size()), "stored"
	return result, contracts.Warning{}, nil
}

func (s *Service) readStream(runID, stream string, offset, limit uint64) (StreamResult, error) {
	path := filepath.Join(s.stateDir, "runs", runID, stream+".log")
	file, err := os.Open(path)
	if err != nil {
		return StreamResult{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return StreamResult{}, err
	}
	if offset > uint64(info.Size()) {
		return StreamResult{}, errors.New("stream offset exceeds observed size")
	}
	if _, err := file.Seek(int64(offset), io.SeekStart); err != nil {
		return StreamResult{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)))
	if err != nil {
		return StreamResult{}, err
	}
	next := offset + uint64(len(data))
	return StreamResult{Ref: "stream:" + runID + ":" + stream, Offset: offset, NextOffset: next, BytesBase64: base64.StdEncoding.EncodeToString(data), EOF: next >= uint64(info.Size()), Truncated: next < uint64(info.Size()), Availability: "stored"}, nil
}

func (s *Service) loadRun(ctx context.Context, id string) (store.Record, runPayload, error) {
	record, err := s.reader.GetRecord(ctx, s.projectID, s.workspaceID, id)
	if err != nil {
		return store.Record{}, runPayload{}, err
	}
	if record.Kind != "run" || record.WriterClass != "server" {
		return store.Record{}, runPayload{}, store.ErrNotFound
	}
	var payload runPayload
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		return store.Record{}, runPayload{}, err
	}
	return record, payload, nil
}

func (s *Service) updateRun(ctx context.Context, record store.Record, payload runPayload, validity string) (store.Record, error) {
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > maximumPlanPayload {
		return store.Record{}, contracts.ErrLimitExceeded
	}
	return s.writer.UpdateObservation(ctx, "run", store.RecordUpdate{
		ProjectID: s.projectID, WorkspaceID: s.workspaceID, ID: record.ID,
		ExpectedRevision: record.Revision, Payload: encoded, EvidenceRefs: record.EvidenceRefs,
		Validity: validity,
	})
}

func validRunID(id string) bool {
	return validRecordID(id, "run_")
}

func validArtifactID(id string) bool {
	return validRecordID(id, "artifact_")
}

func validRecordID(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+32 {
		return false
	}
	for _, character := range id[len(prefix):] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func (s *Service) Close() error {
	s.mu.Lock()
	running := make([]*runningProcess, 0, len(s.running))
	for _, process := range s.running {
		process.cancel()
		running = append(running, process)
	}
	s.mu.Unlock()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for _, process := range running {
		select {
		case <-process.done:
		case <-timer.C:
			return errors.New("runner shutdown timed out")
		}
	}
	return nil
}

// Recover marks runs whose process ownership was lost across a server restart
// as interrupted, then applies bounded stream retention maintenance.
func (s *Service) Recover(ctx context.Context) error {
	s.recoverCacheSwaps()
	page, err := s.reader.QueryRecords(ctx, store.RecordQuery{ProjectID: s.projectID, WorkspaceID: s.workspaceID, Kind: "run", Limit: 10_000})
	if err != nil {
		return err
	}
	for _, record := range page.Records {
		var payload runPayload
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return err
		}
		if payload.State != "running" && payload.State != "materializing" {
			continue
		}
		finished := s.now().UTC()
		payload.State = "interrupted"
		payload.FinishedAt = &finished
		payload.TerminationReason = "server_restart"
		payload.ObservationPartial = true
		if _, err := s.updateRun(ctx, record, payload, "unknown"); err != nil {
			return err
		}
	}
	return s.cleanupExpiredRuns(ctx, s.now().UTC(), 64)
}
