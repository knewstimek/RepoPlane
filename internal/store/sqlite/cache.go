package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"repoplane/internal/store"
)

func (r *Repository) GetCacheEntry(ctx context.Context, projectID, workspaceID, key string) (store.CacheEntry, error) {
	return scanCacheEntry(r.db.QueryRowContext(ctx, cacheSelect+` WHERE project_id=? AND workspace_id=? AND cache_key=?`, projectID, workspaceID, key))
}

func (r *Repository) PublishCacheObservation(ctx context.Context, entry store.CacheEntry) (store.CacheEntry, error) {
	sort.Slice(entry.Outputs, func(i, j int) bool { return entry.Outputs[i].Path < entry.Outputs[j].Path })
	if err := validateCacheEntry(entry); err != nil {
		return store.CacheEntry{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return store.CacheEntry{}, err
	}
	defer tx.Rollback()
	current, err := scanCacheEntry(tx.QueryRowContext(ctx, cacheSelect+` WHERE project_id=? AND workspace_id=? AND cache_key=?`, entry.ProjectID, entry.WorkspaceID, entry.Key))
	if errors.Is(err, store.ErrNotFound) {
		outputs, _ := json.Marshal(entry.Outputs)
		refs, _ := json.Marshal(entry.QualificationRefs)
		_, err = tx.ExecContext(ctx, `INSERT INTO cache_entries(cache_key, project_id, workspace_id, capability_id, capability_revision, configuration, state, reason, source_run_ref, outputs_json, qualification_refs_json, created_at, observed_at, last_used_at, expires_at, observation_count, hit_count) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			entry.Key, entry.ProjectID, entry.WorkspaceID, entry.CapabilityID, entry.CapabilityRevision, entry.Configuration, entry.State, entry.Reason, entry.SourceRunRef, outputs, refs, unixNano(entry.CreatedAt), unixNano(entry.ObservedAt), unixNano(entry.LastUsedAt), unixNano(entry.ExpiresAt), entry.ObservationCount, entry.HitCount)
		if err != nil {
			return store.CacheEntry{}, fmt.Errorf("insert cache entry: %w: %v", store.ErrConflict, err)
		}
		if err := tx.Commit(); err != nil {
			return store.CacheEntry{}, err
		}
		return entry, nil
	}
	if err != nil {
		return store.CacheEntry{}, err
	}
	current.ObservedAt = entry.ObservedAt
	current.ExpiresAt = entry.ExpiresAt
	current.ObservationCount++
	if current.State != "quarantined" && !sameCacheOutputs(current.Outputs, entry.Outputs) {
		current.State = "quarantined"
		current.Reason = "output_mismatch"
	}
	if current.State != "quarantined" && len(entry.QualificationRefs) > 0 {
		current.QualificationRefs = append([]string(nil), entry.QualificationRefs...)
	}
	outputs, _ := json.Marshal(current.Outputs)
	refs, _ := json.Marshal(current.QualificationRefs)
	_, err = tx.ExecContext(ctx, `UPDATE cache_entries SET state=?, reason=?, outputs_json=?, qualification_refs_json=?, observed_at=?, expires_at=?, observation_count=? WHERE project_id=? AND workspace_id=? AND cache_key=?`, current.State, current.Reason, outputs, refs, unixNano(current.ObservedAt), unixNano(current.ExpiresAt), current.ObservationCount, current.ProjectID, current.WorkspaceID, current.Key)
	if err != nil {
		return store.CacheEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return store.CacheEntry{}, err
	}
	return current, nil
}

func sameCacheOutputs(left, right []store.CacheOutput) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Path != right[index].Path || left[index].ContentHash != right[index].ContentHash || left[index].Size != right[index].Size {
			return false
		}
	}
	return true
}

func (r *Repository) MarkCacheHit(ctx context.Context, projectID, workspaceID, key string, usedAt time.Time) (store.CacheEntry, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE cache_entries SET last_used_at=?, hit_count=hit_count+1 WHERE project_id=? AND workspace_id=? AND cache_key=? AND state='active'`, unixNano(usedAt), projectID, workspaceID, key)
	if err != nil {
		return store.CacheEntry{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return store.CacheEntry{}, store.ErrNotFound
	}
	return r.GetCacheEntry(ctx, projectID, workspaceID, key)
}

func (r *Repository) QuarantineCacheEntry(ctx context.Context, projectID, workspaceID, key, reason string, at time.Time) error {
	if reason == "" || len(reason) > 128 {
		return store.ErrConflict
	}
	result, err := r.db.ExecContext(ctx, `UPDATE cache_entries SET state='quarantined', reason=?, observed_at=? WHERE project_id=? AND workspace_id=? AND cache_key=?`, reason, unixNano(at), projectID, workspaceID, key)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return store.ErrNotFound
	}
	return nil
}

func (r *Repository) ListProtectedCacheHashes(ctx context.Context, projectID, workspaceID string, now time.Time, limit uint64) ([]string, bool, error) {
	if limit == 0 {
		return []string{}, false, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT outputs_json FROM cache_entries WHERE project_id=? AND workspace_id=? AND state='active' AND expires_at>? ORDER BY cache_key LIMIT ?`, projectID, workspaceID, unixNano(now), limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	hashes := make(map[string]struct{})
	count := uint64(0)
	for rows.Next() {
		count++
		if count > limit {
			continue
		}
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, false, err
		}
		var outputs []store.CacheOutput
		if err := json.Unmarshal(raw, &outputs); err != nil {
			return nil, false, err
		}
		for _, output := range outputs {
			hashes[output.ContentHash] = struct{}{}
		}
	}
	result := make([]string, 0, len(hashes))
	for hash := range hashes {
		result = append(result, hash)
	}
	sort.Strings(result)
	return result, count <= limit, rows.Err()
}

func (r *Repository) DeleteExpiredCacheEntries(ctx context.Context, projectID, workspaceID string, now time.Time, limit uint64) (uint64, error) {
	if limit == 0 {
		return 0, nil
	}
	result, err := r.db.ExecContext(ctx, `DELETE FROM cache_entries WHERE rowid IN (SELECT rowid FROM cache_entries WHERE project_id=? AND workspace_id=? AND expires_at<=? ORDER BY expires_at, cache_key LIMIT ?)`, projectID, workspaceID, unixNano(now), limit)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return uint64(count), err
}

const cacheSelect = `SELECT cache_key, project_id, workspace_id, capability_id, capability_revision, configuration, state, reason, source_run_ref, outputs_json, qualification_refs_json, created_at, observed_at, last_used_at, expires_at, observation_count, hit_count FROM cache_entries`

type cacheScanner interface{ Scan(...any) error }

func scanCacheEntry(row cacheScanner) (store.CacheEntry, error) {
	var entry store.CacheEntry
	var outputs, refs []byte
	var created, observed, used, expires int64
	err := row.Scan(&entry.Key, &entry.ProjectID, &entry.WorkspaceID, &entry.CapabilityID, &entry.CapabilityRevision, &entry.Configuration, &entry.State, &entry.Reason, &entry.SourceRunRef, &outputs, &refs, &created, &observed, &used, &expires, &entry.ObservationCount, &entry.HitCount)
	if errors.Is(err, sql.ErrNoRows) {
		return store.CacheEntry{}, store.ErrNotFound
	}
	if err != nil {
		return store.CacheEntry{}, err
	}
	if err := json.Unmarshal(outputs, &entry.Outputs); err != nil {
		return store.CacheEntry{}, err
	}
	if err := json.Unmarshal(refs, &entry.QualificationRefs); err != nil {
		return store.CacheEntry{}, err
	}
	entry.CreatedAt, entry.ObservedAt, entry.LastUsedAt, entry.ExpiresAt = time.Unix(0, created).UTC(), time.Unix(0, observed).UTC(), time.Unix(0, used).UTC(), time.Unix(0, expires).UTC()
	return entry, nil
}

func validateCacheEntry(entry store.CacheEntry) error {
	if !strings.HasPrefix(entry.Key, "hmac-sha256:") || len(entry.Key) != len("hmac-sha256:")+64 || entry.ProjectID == "" || entry.WorkspaceID == "" || entry.CapabilityID == "" || entry.CapabilityRevision == "" || entry.Configuration == "" || entry.State != "active" || entry.SourceRunRef == "" || len(entry.Outputs) == 0 || entry.CreatedAt.IsZero() || entry.ObservedAt.IsZero() || entry.ExpiresAt.IsZero() || entry.ObservationCount == 0 {
		return store.ErrConflict
	}
	for index, output := range entry.Outputs {
		if output.Path == "" || !strings.HasPrefix(output.ContentHash, "sha256:") || output.Size < 0 || output.ArtifactRef == "" {
			return store.ErrConflict
		}
		if index > 0 && entry.Outputs[index-1].Path == output.Path {
			return store.ErrConflict
		}
	}
	return nil
}
