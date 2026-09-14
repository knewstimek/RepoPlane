package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"repoplane/internal/store"
)

func (s *Service) observeArtifacts(ctx context.Context, runID string, payload runPayload) ([]string, map[string]string, bool) {
	after, err := s.snapshotPatterns(ctx, payload.OutputPaths)
	if err != nil {
		return []string{}, map[string]string{}, true
	}
	refs := make([]string, 0)
	partial := false
	var captured uint64
	for _, relative := range sortedKeys(after) {
		if strings.HasPrefix(relative, "pattern:") || (after[relative] == payload.OutputsBefore[relative] && (payload.Cache.Key == "" || !payload.Cache.Eligible)) {
			continue
		}
		path, err := s.root.ResolvePrimaryExisting(filepath.FromSlash(relative))
		if err != nil {
			partial = true
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			partial = true
			continue
		}
		storageState := "metadata_only"
		if payload.ArtifactMode == "capture" {
			if uint64(info.Size()) > s.artifactByteLimit || captured+uint64(info.Size()) > s.runArtifactByteLimit {
				storageState = "not_stored_limit"
				partial = true
			} else if err := s.captureBlob(path, after[relative]); err != nil {
				storageState = "not_stored_error"
				partial = true
			} else {
				storageState = "stored"
				captured += uint64(info.Size())
			}
		}
		id, err := recordID("artifact_")
		if err != nil {
			partial = true
			continue
		}
		now := s.now().UTC()
		artifactPayload, _ := json.Marshal(artifactPayload{
			RunRef: "record:" + runID, Path: relative, ContentHash: after[relative],
			Size: info.Size(), Storage: storageState, Basis: "observed", WriterAttribution: "unknown",
		})
		record, err := s.writer.CreateObservation(ctx, store.RecordCreate{Record: store.Record{
			ID: id, Kind: "artifact", SchemaVersion: "artifact-record.v1",
			ProjectID: s.projectID, WorkspaceID: s.workspaceID, Revision: 1,
			Source: "observed", WriterClass: "server", Validity: "current",
			CreatedAt: now, UpdatedAt: now, Payload: artifactPayload,
			EvidenceRefs: []string{"record:" + runID},
		}})
		if err != nil {
			partial = true
			continue
		}
		refs = append(refs, "record:"+record.ID)
	}
	return refs, after, partial
}

func (s *Service) captureBlob(source, identity string) error {
	if !strings.HasPrefix(identity, "sha256:") || len(identity) != len("sha256:")+64 {
		return errors.New("invalid artifact identity")
	}
	directory := filepath.Join(s.stateDir, "artifacts", "blobs")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	destination := filepath.Join(directory, strings.TrimPrefix(identity, "sha256:"))
	if _, err := os.Stat(destination); err == nil {
		observed, hashErr := hashFile(context.Background(), destination, int64(s.artifactByteLimit))
		if hashErr != nil || observed != identity {
			return errors.New("existing artifact blob failed identity verification")
		}
		return nil
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary, err := os.CreateTemp(directory, "capture-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, io.LimitReader(input, int64(s.artifactByteLimit)+1)); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		if _, statErr := os.Stat(destination); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}

// cleanupExpiredRuns expires at most limit stream directories or unreferenced
// artifact blobs. Durable receipts remain queryable with explicit storage state.
func (s *Service) cleanupExpiredRuns(ctx context.Context, now time.Time, limit int) error {
	if limit <= 0 || limit > 64 {
		return errors.New("invalid cleanup limit")
	}
	allRecords, err := s.reader.QueryRecords(ctx, store.RecordQuery{ProjectID: s.projectID, WorkspaceID: s.workspaceID, Limit: 10_000})
	if err != nil {
		return err
	}
	// An incomplete reference scan cannot prove that an old run is unreferenced.
	if !allRecords.Complete {
		return nil
	}
	protected := make(map[string]struct{})
	for _, record := range allRecords.Records {
		if record.Validity != "current" || record.WriterClass == "server" {
			continue
		}
		for _, ref := range record.EvidenceRefs {
			collectProtectedRef(ref, protected)
		}
		collectProtectedRefs(record.Payload, protected)
	}
	page, err := s.reader.QueryRecords(ctx, store.RecordQuery{ProjectID: s.projectID, WorkspaceID: s.workspaceID, Kind: "run", Limit: 10_000})
	if err != nil {
		return err
	}
	sort.SliceStable(page.Records, func(left, right int) bool {
		return runRetentionTime(page.Records[left]).After(runRetentionTime(page.Records[right]))
	})
	eligibleRuns := make(map[string]struct{})
	cutoff := now.Add(-s.retentionAge)
	for index, record := range page.Records {
		if index >= s.retentionRunCount && runRetentionTime(record).Before(cutoff) && validRunID(record.ID) {
			if _, isProtected := protected[record.ID]; !isProtected {
				eligibleRuns[record.ID] = struct{}{}
			}
		}
	}
	artifactsByRun := make(map[string][]store.Record)
	protectedHashes := make(map[string]struct{})
	if s.cache != nil {
		hashes, complete, cacheErr := s.cache.ListProtectedCacheHashes(ctx, s.projectID, s.workspaceID, now, 10_000)
		if cacheErr != nil || !complete {
			return cacheErr
		}
		for _, hash := range hashes {
			protectedHashes[hash] = struct{}{}
		}
	}
	for _, record := range allRecords.Records {
		if record.Kind != "artifact" || record.WriterClass != "server" {
			continue
		}
		var payload artifactPayload
		if json.Unmarshal(record.Payload, &payload) != nil {
			continue
		}
		runID := strings.TrimPrefix(payload.RunRef, "record:")
		artifactsByRun[runID] = append(artifactsByRun[runID], record)
		_, runEligible := eligibleRuns[runID]
		_, artifactProtected := protected[record.ID]
		if !runEligible || artifactProtected {
			protectedHashes[payload.ContentHash] = struct{}{}
		}
	}
	removed := 0
	for index, record := range page.Records {
		if removed >= limit {
			break
		}
		if index < s.retentionRunCount || !runRetentionTime(record).Before(cutoff) || !validRunID(record.ID) {
			continue
		}
		if _, exists := protected[record.ID]; exists {
			continue
		}
		var payload runPayload
		if err := json.Unmarshal(record.Payload, &payload); err != nil || payload.State == "running" {
			continue
		}
		if payload.StreamsRetained && removed < limit {
			directory := filepath.Join(s.stateDir, "runs", record.ID)
			if err := os.RemoveAll(directory); err == nil {
				payload.StreamsRetained = false
				if _, err := s.updateRun(ctx, record, payload, record.Validity); err != nil {
					return err
				}
				removed++
			}
		}
		for _, artifactRecord := range artifactsByRun[record.ID] {
			if removed >= limit {
				break
			}
			if _, isProtected := protected[artifactRecord.ID]; isProtected {
				continue
			}
			var artifact artifactPayload
			if json.Unmarshal(artifactRecord.Payload, &artifact) != nil || artifact.Storage != "stored" {
				continue
			}
			if _, sharedWithRetained := protectedHashes[artifact.ContentHash]; sharedWithRetained {
				continue
			}
			if !strings.HasPrefix(artifact.ContentHash, "sha256:") || len(artifact.ContentHash) != len("sha256:")+64 {
				continue
			}
			blob := filepath.Join(s.stateDir, "artifacts", "blobs", strings.TrimPrefix(artifact.ContentHash, "sha256:"))
			if err := os.Remove(blob); err != nil && !errors.Is(err, os.ErrNotExist) {
				continue
			}
			artifact.Storage = "deleted"
			encoded, _ := json.Marshal(artifact)
			if _, err := s.writer.UpdateObservation(ctx, "artifact", store.RecordUpdate{
				ProjectID: s.projectID, WorkspaceID: s.workspaceID, ID: artifactRecord.ID,
				ExpectedRevision: artifactRecord.Revision, Payload: encoded,
				EvidenceRefs: artifactRecord.EvidenceRefs, Validity: artifactRecord.Validity,
			}); err != nil {
				return err
			}
			removed++
		}
	}
	if s.cache != nil {
		_, _ = s.cache.DeleteExpiredCacheEntries(ctx, s.projectID, s.workspaceID, now, uint64(limit-removed))
	}
	return nil
}

func runRetentionTime(record store.Record) time.Time {
	var payload runPayload
	if json.Unmarshal(record.Payload, &payload) == nil {
		if payload.FinishedAt != nil {
			return *payload.FinishedAt
		}
		if !payload.PreparedAt.IsZero() {
			return payload.PreparedAt
		}
	}
	return record.UpdatedAt
}

func collectProtectedRefs(payload json.RawMessage, protected map[string]struct{}) {
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return
	}
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case string:
			collectProtectedRef(typed, protected)
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(value)
}

func collectProtectedRef(ref string, protected map[string]struct{}) {
	value := strings.TrimPrefix(ref, "record:")
	if validRunID(value) || validArtifactID(value) {
		protected[value] = struct{}{}
	}
}
