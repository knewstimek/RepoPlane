package runner

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"repoplane/internal/catalog"
	"repoplane/internal/contracts"
	"repoplane/internal/store"
)

const cacheEntryTTL = 30 * 24 * time.Hour

func (s *Service) prepareCache(ctx context.Context, capability catalog.Capability, request PrepareRequest, argv []string, inputs map[string]string, checks []PreflightResult) (CacheDecision, map[string]string, error) {
	policy := capability.Manifest.CachePolicy
	if policy == "" {
		policy = "disabled"
	}
	decision := CacheDecision{Policy: policy, Mode: request.CacheMode, Status: "rejected", Reason: "policy_disabled", QualificationRefs: []string{}}
	if policy == "disabled" {
		decision.Status = "disabled"
		return decision, map[string]string{}, nil
	}
	if s.cache == nil || len(s.cacheKey) != 32 {
		decision.Reason = "host_cache_disabled"
		return decision, map[string]string{}, nil
	}
	manifest := capability.Manifest
	if manifest.Cache == nil {
		decision.Reason = "manifest_ineligible"
		return decision, map[string]string{}, nil
	}
	identities := make(map[string]string, len(manifest.Cache.KeyChecks))
	byID := make(map[string]PreflightResult, len(checks))
	for _, check := range checks {
		byID[check.ID] = check
	}
	if executable := byID["runner.executable"]; executable.Identity != "" {
		identities["runner.executable"] = executable.Identity
	} else {
		decision.Reason = "executable_identity_unavailable"
		return decision, identities, nil
	}
	for _, id := range manifest.Cache.KeyChecks {
		check, ok := byID[id]
		if !ok || check.Status != "passed" || check.Identity == "" {
			decision.Reason = "key_identity_unavailable"
			return decision, identities, nil
		}
		identities[id] = check.Identity
	}
	if manifest.Cache.RestorePolicy == "replace_isolated_root" {
		if err := s.validateIsolatedRoot(ctx, manifest); err != nil {
			decision.Reason = "isolated_root_unproven"
			return decision, identities, nil
		}
	}
	key, err := makeCacheKey(s.cacheKey, capability, request.Configuration, argv, inputs, identities)
	if err != nil {
		decision.Reason = "key_unavailable"
		return decision, identities, nil
	}
	decision.Key, decision.Eligible = key, true
	qualified := policy != "verified"
	if policy == "verified" {
		if s.qualifications == nil {
			decision.Eligible, decision.Reason = false, "qualification_unavailable"
			return decision, identities, nil
		}
		refs, ok, err := s.qualifications.ResolveCacheQualifications(ctx, manifest.ID, request.Configuration, manifest.Cache.QualificationChecks)
		if err != nil {
			return CacheDecision{}, nil, err
		}
		decision.QualificationRefs = refs
		qualified = ok
		if !ok {
			decision.Eligible, decision.Reason = false, "qualification_not_current"
			return decision, identities, nil
		}
	}
	if request.CacheMode == "bypass" {
		decision.Status, decision.Reason = "bypassed", "request_bypass"
		return decision, identities, nil
	}
	if policy != "verified" || !qualified {
		decision.Status, decision.Reason = "miss", "observe_only"
		return decision, identities, nil
	}
	entry, err := s.cache.GetCacheEntry(ctx, s.projectID, s.workspaceID, key)
	if errors.Is(err, store.ErrNotFound) {
		decision.Status, decision.Reason = "miss", "entry_not_found"
		return decision, identities, nil
	}
	if err != nil {
		return CacheDecision{}, nil, err
	}
	if entry.State == "quarantined" {
		decision.Status, decision.Reason, decision.SourceRunRef = "quarantined", entry.Reason, entry.SourceRunRef
		return decision, identities, nil
	}
	if entry.State != "active" || !entry.ExpiresAt.After(s.now().UTC()) || entry.CapabilityID != manifest.ID || entry.CapabilityRevision != capability.Revision || entry.Configuration != request.Configuration {
		decision.Status, decision.Reason = "miss", "entry_stale"
		return decision, identities, nil
	}
	if !equalStrings(entry.QualificationRefs, decision.QualificationRefs) {
		decision.Status, decision.Reason = "miss", "qualification_changed"
		return decision, identities, nil
	}
	if err := s.verifyCacheOutputs(ctx, entry.Outputs); err != nil {
		_ = s.cache.QuarantineCacheEntry(context.Background(), s.projectID, s.workspaceID, key, "artifact_unavailable", s.now().UTC())
		decision.Status, decision.Reason = "quarantined", "artifact_unavailable"
		return decision, identities, nil
	}
	decision.Status, decision.Reason, decision.SourceRunRef = "hit", "verified_entry", entry.SourceRunRef
	return decision, identities, nil
}

func makeCacheKey(secret []byte, capability catalog.Capability, configuration string, argv []string, inputs, checks map[string]string) (string, error) {
	if len(secret) != 32 || capability.Manifest.Cache == nil {
		return "", errors.New("cache key prerequisites unavailable")
	}
	hasher := hmac.New(sha256.New, secret)
	write := func(value string) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hasher.Write(length[:])
		_, _ = hasher.Write([]byte(value))
	}
	write("repoplane-cache-key.v1")
	write(fmt.Sprint(capability.Manifest.Cache.ContractRevision))
	write(capability.Manifest.Cache.OutputContract)
	write(capability.Manifest.ID)
	write(capability.Revision)
	write(capability.SourceRef)
	write(capability.ExecutionFingerprint)
	write(configuration)
	for _, value := range argv {
		write(value)
	}
	for _, key := range sortedKeys(inputs) {
		write(key)
		write(inputs[key])
	}
	for _, key := range sortedKeys(checks) {
		write(key)
		write(checks[key])
	}
	return "hmac-sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func (s *Service) verifyCacheOutputs(ctx context.Context, outputs []store.CacheOutput) error {
	if len(outputs) == 0 || len(outputs) > maximumInputFiles {
		return errors.New("cache output set invalid")
	}
	for _, output := range outputs {
		if filepath.IsAbs(output.Path) || strings.HasPrefix(filepath.Clean(filepath.FromSlash(output.Path)), "..") || !strings.HasPrefix(output.ContentHash, "sha256:") {
			return errors.New("cache output invalid")
		}
		blob := filepath.Join(s.stateDir, "artifacts", "blobs", strings.TrimPrefix(output.ContentHash, "sha256:"))
		identity, err := hashFile(ctx, blob, int64(s.artifactByteLimit))
		if err != nil || identity != output.ContentHash {
			return errors.New("cache artifact unavailable")
		}
	}
	return nil
}

func (s *Service) cacheOutputs(ctx context.Context, refs []string) ([]store.CacheOutput, error) {
	outputs := make([]store.CacheOutput, 0, len(refs))
	for _, ref := range refs {
		id := strings.TrimPrefix(ref, "record:")
		record, err := s.reader.GetRecord(ctx, s.projectID, s.workspaceID, id)
		if err != nil || record.Kind != "artifact" {
			return nil, errors.New("cache artifact record unavailable")
		}
		var artifact artifactPayload
		if json.Unmarshal(record.Payload, &artifact) != nil || artifact.Storage != "stored" {
			return nil, errors.New("cache artifact bytes unavailable")
		}
		outputs = append(outputs, store.CacheOutput{Path: artifact.Path, ContentHash: artifact.ContentHash, Size: artifact.Size, ArtifactRef: ref})
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Path < outputs[j].Path })
	return outputs, nil
}

func (s *Service) publishCacheObservation(ctx context.Context, runID string, payload *runPayload) {
	if s.cache == nil || payload.Cache.Key == "" || !payload.Cache.Eligible || payload.State != "completed" || payload.ObservationPartial {
		return
	}
	for path := range payload.OutputsAfter {
		if strings.HasPrefix(path, "pattern:") {
			payload.Cache.Status, payload.Cache.Reason = "rejected", "declared_output_missing"
			return
		}
	}
	outputs, err := s.cacheOutputs(ctx, payload.ArtifactRefs)
	if err != nil || len(outputs) == 0 {
		payload.Cache.Status, payload.Cache.Reason = "rejected", "outputs_not_captured"
		return
	}
	now := s.now().UTC()
	entry, err := s.cache.PublishCacheObservation(ctx, store.CacheEntry{
		Key: payload.Cache.Key, ProjectID: s.projectID, WorkspaceID: s.workspaceID,
		CapabilityID: payload.CapabilityID, CapabilityRevision: payload.CapabilityRevision, Configuration: payload.Configuration,
		State: "active", SourceRunRef: "record:" + runID, Outputs: outputs, QualificationRefs: payload.Cache.QualificationRefs,
		CreatedAt: now, ObservedAt: now, LastUsedAt: now, ExpiresAt: now.Add(cacheEntryTTL), ObservationCount: 1,
	})
	if err != nil {
		payload.Cache.Status, payload.Cache.Reason = "rejected", "publish_failed"
		return
	}
	if entry.State == "quarantined" {
		payload.Cache.Status, payload.Cache.Reason = "quarantined", entry.Reason
		return
	}
	payload.Cache.Status, payload.Cache.Reason, payload.Cache.SourceRunRef = "observed", "entry_recorded", entry.SourceRunRef
}

func (s *Service) executeCacheHit(ctx context.Context, record store.Record, payload *runPayload, capability catalog.Capability) (ExecuteResponse, error) {
	manifest := capability.Manifest
	identities := make(map[string]string, len(payload.CacheKeyChecks))
	identities["runner.executable"] = payload.ExecutableIdentity
	declarations := make(map[string]catalog.PreflightCheck, len(manifest.Execution.Preflight))
	for _, declaration := range manifest.Execution.Preflight {
		declarations[declaration.ID] = declaration
	}
	for id, expected := range payload.CacheKeyChecks {
		if id == "runner.executable" {
			if expected != payload.ExecutableIdentity {
				return ExecuteResponse{}, ErrPlanStale
			}
			continue
		}
		declaration, ok := declarations[id]
		if !ok {
			return ExecuteResponse{}, ErrPlanStale
		}
		check := s.runPreflightCheck(ctx, declaration)
		if check.Status != "passed" || check.Identity != expected {
			return ExecuteResponse{}, ErrPlanStale
		}
		identities[id] = expected
	}
	key, err := makeCacheKey(s.cacheKey, capability, payload.Configuration, payload.Argv, payload.InputHashes, identities)
	if err != nil || key != payload.Cache.Key {
		return ExecuteResponse{}, ErrPlanStale
	}
	refs, qualified, err := s.qualifications.ResolveCacheQualifications(ctx, payload.CapabilityID, payload.Configuration, manifest.Cache.QualificationChecks)
	if err != nil || !qualified || !equalStrings(refs, payload.Cache.QualificationRefs) {
		return ExecuteResponse{}, ErrPlanStale
	}
	entry, err := s.cache.GetCacheEntry(ctx, s.projectID, s.workspaceID, key)
	if err != nil || entry.State != "active" || !entry.ExpiresAt.After(s.now().UTC()) || entry.SourceRunRef != payload.Cache.SourceRunRef || !equalStrings(entry.QualificationRefs, payload.Cache.QualificationRefs) {
		return ExecuteResponse{}, ErrPlanStale
	}
	if err := s.verifyCacheOutputs(ctx, entry.Outputs); err != nil {
		_ = s.cache.QuarantineCacheEntry(context.Background(), s.projectID, s.workspaceID, key, "artifact_unavailable", s.now().UTC())
		return ExecuteResponse{}, ErrPlanStale
	}
	started := s.now().UTC()
	payload.State, payload.StartedAt = "materializing", &started
	materializingRecord, err := s.updateRun(ctx, record, *payload, "current")
	if err != nil {
		return ExecuteResponse{}, err
	}
	failMaterialization := func() (ExecuteResponse, error) {
		finished := s.now().UTC()
		payload.State, payload.FinishedAt = "failed", &finished
		payload.TerminationReason, payload.ObservationPartial = "cache_materialization_failed", true
		payload.Cache.Status, payload.Cache.Reason = "rejected", "materialization_failed"
		_, _ = s.updateRun(context.Background(), materializingRecord, *payload, "unknown")
		return ExecuteResponse{}, ErrPlanStale
	}
	created := make([]string, 0, len(entry.Outputs))
	if manifest.Cache.RestorePolicy == "replace_isolated_root" {
		if err := s.materializeIsolatedRoot(ctx, manifest.Cache.IsolatedRoot, entry.Outputs); err != nil {
			return failMaterialization()
		}
	} else {
		for _, output := range entry.Outputs {
			blob := filepath.Join(s.stateDir, "artifacts", "blobs", strings.TrimPrefix(output.ContentHash, "sha256:"))
			destination, err := s.resolveOutputDestination(output.Path)
			if err != nil {
				s.rollbackMaterialized(created)
				return failMaterialization()
			}
			wrote, err := copyVerifiedFile(blob, destination, output.ContentHash)
			if err != nil {
				s.rollbackMaterialized(created)
				return failMaterialization()
			}
			if wrote {
				created = append(created, destination)
			}
		}
	}
	now := s.now().UTC()
	payload.State, payload.StartedAt, payload.FinishedAt = "reused", &now, &now
	payload.TerminationReason = "cache_hit"
	exitCode := 0
	payload.ExitCode = &exitCode
	payload.StreamsRetained = false
	payload.OutputsAfter, err = s.snapshotPatterns(ctx, payload.OutputPaths)
	if err != nil {
		s.rollbackMaterialized(created)
		return ExecuteResponse{}, err
	}
	payload.ArtifactRefs, payload.ObservationPartial = s.recordReusedArtifacts(ctx, record.ID, entry.Outputs)
	payload.Cache.Status, payload.Cache.Reason = "reused", "verified_entry_materialized"
	updated, err := s.updateRun(ctx, materializingRecord, *payload, "current")
	if err != nil {
		return ExecuteResponse{}, err
	}
	_, _ = s.cache.MarkCacheHit(context.Background(), s.projectID, s.workspaceID, key, now)
	_ = updated
	return ExecuteResponse{Status: "ok", RunID: record.ID, State: "reused", Warnings: []contracts.Warning{}}, nil
}

func (s *Service) resolveOutputDestination(relative string) (string, error) {
	destination, _, err := s.root.ResolveForLookup(filepath.FromSlash(relative))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return "", err
	}
	rechecked, _, err := s.root.ResolveForLookup(filepath.FromSlash(relative))
	if err != nil || !samePath(destination, rechecked) {
		return "", errors.New("output parent changed during materialization")
	}
	return rechecked, nil
}

func (s *Service) recordReusedArtifacts(ctx context.Context, runID string, outputs []store.CacheOutput) ([]string, bool) {
	refs := make([]string, 0, len(outputs))
	partial := false
	for _, output := range outputs {
		id, err := recordID("artifact_")
		if err != nil {
			partial = true
			continue
		}
		now := s.now().UTC()
		encoded, _ := json.Marshal(artifactPayload{RunRef: "record:" + runID, Path: output.Path, ContentHash: output.ContentHash, Size: output.Size, Storage: "stored", Basis: "reused", WriterAttribution: "cache"})
		record, err := s.writer.CreateObservation(ctx, store.RecordCreate{Record: store.Record{ID: id, Kind: "artifact", SchemaVersion: "artifact-record.v1", ProjectID: s.projectID, WorkspaceID: s.workspaceID, Revision: 1, Source: "observed", WriterClass: "server", Validity: "current", CreatedAt: now, UpdatedAt: now, Payload: encoded, EvidenceRefs: []string{"record:" + runID, output.ArtifactRef}}})
		if err != nil {
			partial = true
			continue
		}
		refs = append(refs, "record:"+record.ID)
	}
	return refs, partial
}

func (s *Service) rollbackMaterialized(paths []string) {
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (s *Service) validateIsolatedRoot(ctx context.Context, manifest catalog.Manifest) error {
	root := strings.TrimSuffix(filepath.ToSlash(manifest.Cache.IsolatedRoot), "/")
	resolved, _, err := s.root.ResolveForLookup(filepath.FromSlash(root))
	if err != nil || !samePath(resolved, filepath.Join(s.root.Resolved(), filepath.FromSlash(root))) {
		return errors.New("isolated root may not traverse a link")
	}
	for _, input := range manifest.Inputs {
		input = filepath.ToSlash(input)
		if input == root || strings.HasPrefix(input, root+"/") || strings.HasPrefix(root, strings.TrimSuffix(input, "/")+"/") {
			return errors.New("isolated root overlaps declared input")
		}
	}
	command := exec.CommandContext(ctx, "git", "-C", s.root.Resolved(), "ls-files", "--", root)
	output, err := boundedCommandOutput(command, maximumProbeOutput)
	if err != nil || len(strings.TrimSpace(string(output))) != 0 {
		return errors.New("isolated root tracked state is not empty")
	}
	return nil
}

type cacheSwapMarker struct {
	Root   string `json:"root"`
	Backup string `json:"backup"`
}

func (s *Service) materializeIsolatedRoot(ctx context.Context, isolated string, outputs []store.CacheOutput) error {
	root, _, err := s.root.ResolveForLookup(filepath.FromSlash(isolated))
	if err != nil {
		return err
	}
	lexical := filepath.Clean(filepath.Join(s.root.Resolved(), filepath.FromSlash(isolated)))
	if !samePath(root, lexical) {
		return errors.New("isolated root may not traverse a link")
	}
	parent := filepath.Dir(root)
	stage, err := os.MkdirTemp(parent, ".repoplane-cache-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	prefix := strings.TrimSuffix(filepath.ToSlash(isolated), "/") + "/"
	for _, output := range outputs {
		if !strings.HasPrefix(output.Path, prefix) {
			return errors.New("cache output escaped isolated root")
		}
		relative := strings.TrimPrefix(output.Path, prefix)
		destination := filepath.Join(stage, filepath.FromSlash(relative))
		if rel, relErr := filepath.Rel(stage, destination); relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("cache output escaped staging root")
		}
		blob := filepath.Join(s.stateDir, "artifacts", "blobs", strings.TrimPrefix(output.ContentHash, "sha256:"))
		if _, err := copyVerifiedFile(blob, destination, output.ContentHash); err != nil {
			return err
		}
	}
	backup := root + ".old_repoplane_cache_" + s.now().UTC().Format("20060102T150405.000000000")
	markerDir := filepath.Join(s.stateDir, "cache-swaps")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		return err
	}
	markerName := filepath.Join(markerDir, filepath.Base(stage)+".json")
	markerBytes, _ := json.Marshal(cacheSwapMarker{Root: root, Backup: backup})
	if err := os.WriteFile(markerName, markerBytes, 0o600); err != nil {
		return err
	}
	defer os.Remove(markerName)
	exists := false
	if info, statErr := os.Lstat(root); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("isolated root is not a plain directory")
		}
		exists = true
		if err := os.Rename(root, backup); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err := os.Rename(stage, root); err != nil {
		if exists {
			_ = os.Rename(backup, root)
		}
		return err
	}
	if exists {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func samePath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func (s *Service) recoverCacheSwaps() {
	directory := filepath.Join(s.stateDir, "cache-swaps")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for index, entry := range entries {
		if index >= 64 || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			break
		}
		path := filepath.Join(directory, entry.Name())
		data, err := os.ReadFile(path)
		var marker cacheSwapMarker
		if err != nil || json.Unmarshal(data, &marker) != nil || !pathWithin(s.root.Resolved(), marker.Root) || !pathWithin(s.root.Resolved(), marker.Backup) || filepath.Dir(marker.Root) != filepath.Dir(marker.Backup) || !strings.Contains(filepath.Base(marker.Backup), ".old_repoplane_cache_") {
			continue
		}
		_, rootErr := os.Lstat(marker.Root)
		_, backupErr := os.Lstat(marker.Backup)
		if errors.Is(rootErr, os.ErrNotExist) && backupErr == nil {
			_ = os.Rename(marker.Backup, marker.Root)
		} else if rootErr == nil && backupErr == nil {
			_ = os.RemoveAll(marker.Backup)
		}
		_ = os.Remove(path)
	}
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func copyVerifiedFile(source, destination, identity string) (bool, error) {
	if current, err := hashFile(context.Background(), destination, 512*1024*1024); err == nil {
		if current == identity {
			return false, nil
		}
		return false, errors.New("output_conflict")
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return false, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".repoplane-cache-*")
	if err != nil {
		return false, err
	}
	name := temporary.Name()
	defer os.Remove(name)
	input, err := os.Open(source)
	if err != nil {
		_ = temporary.Close()
		return false, err
	}
	_, copyErr := temporary.ReadFrom(input)
	closeIn, closeOut := input.Close(), temporary.Close()
	if copyErr != nil || closeIn != nil || closeOut != nil {
		return false, errors.Join(copyErr, closeIn, closeOut)
	}
	observed, err := hashFile(context.Background(), name, 512*1024*1024)
	if err != nil || observed != identity {
		return false, errors.New("materialized hash mismatch")
	}
	if err := os.Rename(name, destination); err != nil {
		return false, err
	}
	return true, nil
}
