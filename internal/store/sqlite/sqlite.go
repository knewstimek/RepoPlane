// Package sqlite implements RepoPlane persistence using a local SQLite file.
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

	_ "modernc.org/sqlite"

	"repoplane/internal/store"
)

var _ store.Repository = (*Repository)(nil)

type Repository struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Repository, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	repository := &Repository{db: db}
	if err := repository.initialize(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return repository, nil
}

func (r *Repository) Close() error { return r.db.Close() }

const currentSchemaVersion = 2

func (r *Repository) initialize(ctx context.Context) (err error) {
	for _, statement := range []string{`PRAGMA foreign_keys = ON`, `PRAGMA busy_timeout = 5000`} {
		if _, err := r.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sqlite migration: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
            version INTEGER PRIMARY KEY,
            applied_at INTEGER NOT NULL
        )`); err != nil {
		return fmt.Errorf("initialize migration ledger: %w", err)
	}
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read sqlite schema version: %w", err)
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("sqlite schema version %d is newer than supported version %d", version, currentSchemaVersion)
	}
	if version < 1 {
		if err = migrateV1(ctx, tx); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (1, ?)`, time.Now().UTC().UnixNano()); err != nil {
			return fmt.Errorf("record sqlite migration 1: %w", err)
		}
	}
	if version < 2 {
		if err = migrateV2(ctx, tx); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (2, ?)`, time.Now().UTC().UnixNano()); err != nil {
			return fmt.Errorf("record sqlite migration 2: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite migrations: %w", err)
	}
	return nil
}

func migrateV1(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS workspaces (
            id TEXT PRIMARY KEY,
            root_fingerprint TEXT NOT NULL,
            created_at INTEGER NOT NULL,
            last_seen_at INTEGER NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS catalog_generations (
            id TEXT PRIMARY KEY,
            workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
            source_fingerprint TEXT NOT NULL,
            created_at INTEGER NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS current_catalog (
            workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
            generation_id TEXT NOT NULL REFERENCES catalog_generations(id) ON DELETE CASCADE
        )`,
		`CREATE TABLE IF NOT EXISTS catalog_items (
            generation_id TEXT NOT NULL REFERENCES catalog_generations(id) ON DELETE CASCADE,
            id TEXT NOT NULL,
            revision TEXT NOT NULL,
            source_ref TEXT NOT NULL,
            document_json BLOB NOT NULL,
            PRIMARY KEY (generation_id, id)
        )`,
		`CREATE TABLE IF NOT EXISTS catalog_terms (
            generation_id TEXT NOT NULL,
            item_id TEXT NOT NULL,
            field TEXT NOT NULL,
            term TEXT NOT NULL,
            weight INTEGER NOT NULL,
            FOREIGN KEY (generation_id, item_id) REFERENCES catalog_items(generation_id, id) ON DELETE CASCADE
        )`,
		`CREATE INDEX IF NOT EXISTS catalog_terms_lookup ON catalog_terms(generation_id, item_id)`,
		`CREATE TABLE IF NOT EXISTS catalog_issues (
            generation_id TEXT NOT NULL REFERENCES catalog_generations(id) ON DELETE CASCADE,
            ordinal INTEGER NOT NULL,
            code TEXT NOT NULL,
            source_ref TEXT NOT NULL,
            detail_json BLOB NOT NULL,
            PRIMARY KEY (generation_id, ordinal)
        )`,
		`CREATE TABLE IF NOT EXISTS result_sets (
            id TEXT PRIMARY KEY,
            workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
            query_hash TEXT NOT NULL,
            generation_id TEXT NOT NULL,
            created_at INTEGER NOT NULL,
            expires_at INTEGER NOT NULL,
            item_count INTEGER NOT NULL,
            metadata_json BLOB NOT NULL
        )`,
		`CREATE INDEX IF NOT EXISTS result_sets_expiry ON result_sets(expires_at, id)`,
		`CREATE TABLE IF NOT EXISTS result_items (
            result_set_id TEXT NOT NULL REFERENCES result_sets(id) ON DELETE CASCADE,
            ordinal INTEGER NOT NULL,
            item_ref TEXT NOT NULL,
            item_hash TEXT NOT NULL,
            payload_json BLOB NOT NULL,
            PRIMARY KEY (result_set_id, ordinal)
		)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply sqlite migration 1: %w", err)
		}
	}
	// Pre-release databases may have result_sets without these final v1 columns.
	// Repair that shape before declaring migration 1 applied.
	columns, err := tableColumns(ctx, tx, "result_sets")
	if err != nil {
		return err
	}
	if !columns["item_count"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE result_sets ADD COLUMN item_count INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add result_sets.item_count: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE result_sets SET item_count=(SELECT COUNT(*) FROM result_items WHERE result_set_id=result_sets.id)`); err != nil {
			return fmt.Errorf("backfill result_sets.item_count: %w", err)
		}
	}
	if !columns["metadata_json"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE result_sets ADD COLUMN metadata_json BLOB NOT NULL DEFAULT '{}'`); err != nil {
			return fmt.Errorf("add result_sets.metadata_json: %w", err)
		}
	}
	return nil
}

func tableColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("inspect sqlite table %s: %w", table, err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var ordinal int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("inspect sqlite table %s: %w", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect sqlite table %s: %w", table, err)
	}
	return columns, nil
}

func migrateV2(ctx context.Context, tx *sql.Tx) error {
	columns, err := tableColumns(ctx, tx, "catalog_items")
	if err != nil {
		return err
	}
	if !columns["execution_fingerprint"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE catalog_items ADD COLUMN execution_fingerprint TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add catalog_items.execution_fingerprint: %w", err)
		}
	}
	return nil
}

func (r *Repository) UpsertWorkspace(ctx context.Context, workspace store.Workspace) error {
	if workspace.ID == "" || workspace.RootFingerprint == "" {
		return fmt.Errorf("upsert workspace: %w", store.ErrConflict)
	}
	_, err := r.db.ExecContext(ctx, `
        INSERT INTO workspaces(id, root_fingerprint, created_at, last_seen_at)
        VALUES (?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            root_fingerprint=excluded.root_fingerprint,
            last_seen_at=excluded.last_seen_at`,
		workspace.ID, workspace.RootFingerprint, unixNano(workspace.CreatedAt), unixNano(workspace.LastSeenAt))
	if err != nil {
		return fmt.Errorf("upsert workspace: %w", err)
	}
	return nil
}

func (r *Repository) GetWorkspace(ctx context.Context, id string) (store.Workspace, error) {
	var result store.Workspace
	var created, seen int64
	err := r.db.QueryRowContext(ctx, `
        SELECT id, root_fingerprint, created_at, last_seen_at
        FROM workspaces WHERE id=?`, id).Scan(&result.ID, &result.RootFingerprint, &created, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Workspace{}, store.ErrNotFound
	}
	if err != nil {
		return store.Workspace{}, fmt.Errorf("get workspace: %w", err)
	}
	result.CreatedAt = time.Unix(0, created).UTC()
	result.LastSeenAt = time.Unix(0, seen).UTC()
	return result, nil
}

func (r *Repository) PublishCatalogGeneration(ctx context.Context, generation store.CatalogGeneration) (err error) {
	if err := validateGeneration(generation); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin catalog publish: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	meta := generation.Meta
	if _, err = tx.ExecContext(ctx, `
        INSERT INTO catalog_generations(id, workspace_id, source_fingerprint, created_at)
        VALUES (?, ?, ?, ?)`, meta.ID, meta.WorkspaceID, meta.SourceFingerprint, unixNano(meta.CreatedAt)); err != nil {
		return fmt.Errorf("insert catalog generation: %w: %v", store.ErrConflict, err)
	}
	for _, item := range generation.Items {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO catalog_items(generation_id, id, revision, source_ref, execution_fingerprint, document_json)
			VALUES (?, ?, ?, ?, ?, ?)`, meta.ID, item.ID, item.Revision, item.SourceRef, item.ExecutionFingerprint, []byte(item.Document)); err != nil {
			return fmt.Errorf("insert catalog item: %w: %v", store.ErrConflict, err)
		}
		for _, term := range item.Terms {
			if _, err = tx.ExecContext(ctx, `
                INSERT INTO catalog_terms(generation_id, item_id, field, term, weight)
                VALUES (?, ?, ?, ?, ?)`, meta.ID, item.ID, term.Field, term.Term, term.Weight); err != nil {
				return fmt.Errorf("insert catalog term: %w", err)
			}
		}
	}
	for ordinal, issue := range generation.Issues {
		if _, err = tx.ExecContext(ctx, `
            INSERT INTO catalog_issues(generation_id, ordinal, code, source_ref, detail_json)
            VALUES (?, ?, ?, ?, ?)`, meta.ID, ordinal, issue.Code, issue.SourceRef, []byte(issue.Detail)); err != nil {
			return fmt.Errorf("insert catalog issue: %w", err)
		}
	}
	if _, err = tx.ExecContext(ctx, `
        INSERT INTO current_catalog(workspace_id, generation_id) VALUES (?, ?)
        ON CONFLICT(workspace_id) DO UPDATE SET generation_id=excluded.generation_id`, meta.WorkspaceID, meta.ID); err != nil {
		return fmt.Errorf("publish current catalog: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit catalog publish: %w", err)
	}
	return nil
}

func (r *Repository) CurrentCatalogGeneration(ctx context.Context, workspaceID string) (store.CatalogGenerationMeta, error) {
	var result store.CatalogGenerationMeta
	var created int64
	err := r.db.QueryRowContext(ctx, `
        SELECT g.id, g.workspace_id, g.source_fingerprint, g.created_at
        FROM current_catalog c
        JOIN catalog_generations g ON g.id=c.generation_id
        WHERE c.workspace_id=?`, workspaceID).Scan(
		&result.ID, &result.WorkspaceID, &result.SourceFingerprint, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return store.CatalogGenerationMeta{}, store.ErrNotFound
	}
	if err != nil {
		return store.CatalogGenerationMeta{}, fmt.Errorf("get current catalog: %w", err)
	}
	result.CreatedAt = time.Unix(0, created).UTC()
	return result, nil
}

func (r *Repository) GetCatalogItem(ctx context.Context, workspaceID, generationID, itemID string) (store.CatalogItem, error) {
	resolved, err := r.resolveGeneration(ctx, workspaceID, generationID)
	if err != nil {
		return store.CatalogItem{}, err
	}
	var item store.CatalogItem
	err = r.db.QueryRowContext(ctx, `
		SELECT id, revision, source_ref, execution_fingerprint, document_json
		FROM catalog_items WHERE generation_id=? AND id=?`, resolved, itemID).Scan(
		&item.ID, &item.Revision, &item.SourceRef, &item.ExecutionFingerprint, &item.Document)
	if errors.Is(err, sql.ErrNoRows) {
		return store.CatalogItem{}, store.ErrNotFound
	}
	if err != nil {
		return store.CatalogItem{}, fmt.Errorf("get catalog item: %w", err)
	}
	item.Terms, err = r.readTerms(ctx, resolved, item.ID)
	if err != nil {
		return store.CatalogItem{}, err
	}
	return item, nil
}

func (r *Repository) ListCatalogIssues(ctx context.Context, workspaceID, generationID string) ([]store.CatalogIssue, error) {
	resolved, err := r.resolveGeneration(ctx, workspaceID, generationID)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
        SELECT code, source_ref, detail_json FROM catalog_issues
        WHERE generation_id=? ORDER BY ordinal`, resolved)
	if err != nil {
		return nil, fmt.Errorf("list catalog issues: %w", err)
	}
	defer rows.Close()
	issues := make([]store.CatalogIssue, 0)
	for rows.Next() {
		var issue store.CatalogIssue
		if err := rows.Scan(&issue.Code, &issue.SourceRef, &issue.Detail); err != nil {
			return nil, fmt.Errorf("scan catalog issue: %w", err)
		}
		issues = append(issues, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalog issues: %w", err)
	}
	return issues, nil
}

func (r *Repository) SearchCatalog(ctx context.Context, query store.CatalogQuery) (store.CatalogPage, error) {
	if query.Limit == 0 {
		return store.CatalogPage{}, fmt.Errorf("search catalog: limit: %w", store.ErrConflict)
	}
	generationID, err := r.resolveGeneration(ctx, query.WorkspaceID, query.GenerationID)
	if err != nil {
		return store.CatalogPage{}, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, revision, source_ref, execution_fingerprint, document_json
		FROM catalog_items WHERE generation_id=?`, generationID)
	if err != nil {
		return store.CatalogPage{}, fmt.Errorf("search catalog: %w", err)
	}
	items := make([]store.CatalogItem, 0)
	for rows.Next() {
		var item store.CatalogItem
		if err := rows.Scan(&item.ID, &item.Revision, &item.SourceRef, &item.ExecutionFingerprint, &item.Document); err != nil {
			_ = rows.Close()
			return store.CatalogPage{}, fmt.Errorf("scan catalog item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return store.CatalogPage{}, fmt.Errorf("iterate catalog items: %w", err)
	}
	if err := rows.Close(); err != nil {
		return store.CatalogPage{}, fmt.Errorf("close catalog rows: %w", err)
	}

	terms := normalizeTerms(query.Terms)
	matches := make([]store.CatalogMatch, 0)
	for _, item := range items {
		item.Terms, err = r.readTerms(ctx, generationID, item.ID)
		if err != nil {
			return store.CatalogPage{}, err
		}
		exact := query.ExactID != "" && strings.EqualFold(query.ExactID, item.ID)
		score := score(item.Terms, terms)
		if exact || score > 0 || len(terms) == 0 {
			if exact {
				score += 1_000_000
			}
			matches = append(matches, store.CatalogMatch{Item: item, Score: score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Item.ID < matches[j].Item.ID
	})
	matched := uint64(len(matches))
	if matched > query.Limit {
		matches = matches[:query.Limit]
	}
	return store.CatalogPage{GenerationID: generationID, Matches: matches, Matched: &matched, Complete: true}, nil
}

func (r *Repository) CreateResultSet(ctx context.Context, set store.ResultSet) (err error) {
	if err := validateResultSet(set); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin result set: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `
        INSERT INTO result_sets(id, workspace_id, query_hash, generation_id, created_at, expires_at, item_count, metadata_json)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, set.ID, set.WorkspaceID, set.QueryHash, set.GenerationID,
		unixNano(set.CreatedAt), unixNano(set.ExpiresAt), len(set.Items), []byte(set.Metadata)); err != nil {
		return fmt.Errorf("insert result set: %w: %v", store.ErrConflict, err)
	}
	for _, item := range set.Items {
		if _, err = tx.ExecContext(ctx, `
            INSERT INTO result_items(result_set_id, ordinal, item_ref, item_hash, payload_json)
            VALUES (?, ?, ?, ?, ?)`, set.ID, item.Ordinal, item.ItemRef, item.ItemHash, []byte(item.Payload)); err != nil {
			return fmt.Errorf("insert result item: %w: %v", store.ErrConflict, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit result set: %w", err)
	}
	return nil
}

func (r *Repository) ReadResultPage(ctx context.Context, id string, from, limit uint64) (store.ResultPage, error) {
	if limit == 0 {
		return store.ResultPage{}, fmt.Errorf("read result page: limit: %w", store.ErrConflict)
	}
	var total uint64
	var metadata json.RawMessage
	if err := r.db.QueryRowContext(ctx, `SELECT item_count, metadata_json FROM result_sets WHERE id=?`, id).Scan(&total, &metadata); errors.Is(err, sql.ErrNoRows) {
		return store.ResultPage{}, store.ErrNotFound
	} else if err != nil {
		return store.ResultPage{}, fmt.Errorf("read result set: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `
        SELECT ordinal, item_ref, item_hash, payload_json
        FROM result_items WHERE result_set_id=? AND ordinal>=?
        ORDER BY ordinal LIMIT ?`, id, from, limit+1)
	if err != nil {
		return store.ResultPage{}, fmt.Errorf("read result page: %w", err)
	}
	defer rows.Close()
	items := make([]store.ResultItem, 0, limit)
	for rows.Next() {
		var item store.ResultItem
		if err := rows.Scan(&item.Ordinal, &item.ItemRef, &item.ItemHash, &item.Payload); err != nil {
			return store.ResultPage{}, fmt.Errorf("scan result item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return store.ResultPage{}, err
	}
	var next *uint64
	if uint64(len(items)) > limit {
		value := items[limit].Ordinal
		next = &value
		items = items[:limit]
	}
	return store.ResultPage{ResultSetID: id, Items: items, Total: total, Metadata: metadata, NextOrdinal: next}, nil
}

func (r *Repository) DeleteExpiredResultSets(ctx context.Context, now time.Time, limit uint64) (uint64, error) {
	if limit == 0 {
		return 0, nil
	}
	result, err := r.db.ExecContext(ctx, `
        DELETE FROM result_sets WHERE id IN (
            SELECT id FROM result_sets WHERE expires_at<=? ORDER BY expires_at, id LIMIT ?
        )`, unixNano(now), limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired result sets: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count deleted result sets: %w", err)
	}
	return uint64(count), nil
}

func (r *Repository) resolveGeneration(ctx context.Context, workspaceID, generationID string) (string, error) {
	if generationID != "" {
		var exists int
		err := r.db.QueryRowContext(ctx, `
            SELECT 1 FROM catalog_generations WHERE id=? AND workspace_id=?`, generationID, workspaceID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return "", store.ErrNotFound
		}
		if err != nil {
			return "", err
		}
		return generationID, nil
	}
	meta, err := r.CurrentCatalogGeneration(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	return meta.ID, nil
}

func (r *Repository) readTerms(ctx context.Context, generationID, itemID string) ([]store.CatalogTerm, error) {
	rows, err := r.db.QueryContext(ctx, `
        SELECT field, term, weight FROM catalog_terms
        WHERE generation_id=? AND item_id=?
        ORDER BY field, term, weight`, generationID, itemID)
	if err != nil {
		return nil, fmt.Errorf("read catalog terms: %w", err)
	}
	defer rows.Close()
	terms := make([]store.CatalogTerm, 0)
	for rows.Next() {
		var term store.CatalogTerm
		if err := rows.Scan(&term.Field, &term.Term, &term.Weight); err != nil {
			return nil, err
		}
		terms = append(terms, term)
	}
	return terms, rows.Err()
}

func validateGeneration(generation store.CatalogGeneration) error {
	if generation.Meta.ID == "" || generation.Meta.WorkspaceID == "" || generation.Meta.SourceFingerprint == "" || generation.Meta.CreatedAt.IsZero() {
		return fmt.Errorf("catalog generation metadata: %w", store.ErrConflict)
	}
	seen := make(map[string]struct{}, len(generation.Items))
	for _, item := range generation.Items {
		if item.ID == "" || item.Revision == "" || item.SourceRef == "" || !json.Valid(item.Document) {
			return fmt.Errorf("catalog item %q: %w", item.ID, store.ErrConflict)
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return fmt.Errorf("duplicate catalog item %q: %w", item.ID, store.ErrConflict)
		}
		seen[item.ID] = struct{}{}
	}
	for _, issue := range generation.Issues {
		if issue.Code == "" || !json.Valid(issue.Detail) {
			return fmt.Errorf("catalog issue: %w", store.ErrConflict)
		}
	}
	return nil
}

func validateResultSet(set store.ResultSet) error {
	if set.ID == "" || set.WorkspaceID == "" || set.QueryHash == "" || set.GenerationID == "" || set.CreatedAt.IsZero() || !set.ExpiresAt.After(set.CreatedAt) || !json.Valid(set.Metadata) {
		return fmt.Errorf("result set metadata: %w", store.ErrConflict)
	}
	for ordinal, item := range set.Items {
		if item.Ordinal != uint64(ordinal) || item.ItemRef == "" || item.ItemHash == "" || !json.Valid(item.Payload) {
			return fmt.Errorf("result item %d: %w", ordinal, store.ErrConflict)
		}
	}
	return nil
}

func normalizeTerms(terms []string) []string {
	result := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term != "" {
			result = append(result, term)
		}
	}
	return result
}

func score(itemTerms []store.CatalogTerm, queryTerms []string) int64 {
	var result int64
	for _, query := range queryTerms {
		for _, term := range itemTerms {
			if strings.Contains(strings.ToLower(term.Term), query) {
				result += int64(term.Weight)
			}
		}
	}
	return result
}

func unixNano(value time.Time) int64 { return value.UTC().UnixNano() }
