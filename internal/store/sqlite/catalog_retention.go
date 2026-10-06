package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"repoplane/internal/store"
)

// SQL retirement timestamps use integer millisecond arithmetic, not a floating
// point conversion of nanoseconds. Legacy publishers have no injected Go clock.
const catalogRetirementClockSQL = "(CAST(strftime('%s','now') AS INTEGER)*1000 + CAST(substr(strftime('%f','now'),4,3) AS INTEGER))*1000000"

func isRetirementSchemaV4(ctx context.Context, tx *sql.Tx) (bool, error) {
	columns, err := tableColumns(ctx, tx, "catalog_generations")
	if err != nil {
		return false, err
	}
	if len(columns) != 5 || !columns["retired_at"] || !columns["id"] || !columns["workspace_id"] || !columns["source_fingerprint"] || !columns["created_at"] {
		return false, nil
	}
	var indexes, baseVersion int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name IN ('catalog_generations_retired','result_sets_generation_expiry')").Scan(&indexes); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version=3").Scan(&baseVersion); err != nil {
		return false, err
	}
	return indexes == 2 && baseVersion == 1, nil
}

func initializeCatalogRetention(ctx context.Context, tx *sql.Tx, now time.Time, recoverV4 bool) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS catalog_retirements (
			generation_id TEXT PRIMARY KEY REFERENCES catalog_generations(id) ON DELETE CASCADE,
			retired_at INTEGER NOT NULL)`,
		"CREATE INDEX IF NOT EXISTS catalog_retirements_age ON catalog_retirements(retired_at, generation_id)",
		"CREATE INDEX IF NOT EXISTS catalog_generations_workspace ON catalog_generations(workspace_id, id)",
		"CREATE INDEX IF NOT EXISTS result_sets_generation_expiry ON result_sets(generation_id, expires_at)",
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize catalog retention: %w", err)
		}
	}
	if recoverV4 {
		// Preserve known retirement observations and all catalog data while
		// restoring the original four-column table and v3 compatibility marker.
		for _, statement := range []string{
			`INSERT OR IGNORE INTO catalog_retirements SELECT g.id,g.retired_at FROM catalog_generations g
				WHERE g.retired_at IS NOT NULL AND NOT EXISTS (SELECT 1 FROM current_catalog c WHERE c.generation_id=g.id)`,
			"DROP INDEX catalog_generations_retired",
			"ALTER TABLE catalog_generations DROP COLUMN retired_at",
			"DELETE FROM schema_migrations WHERE version=4",
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("recover catalog retention compatibility: %w", err)
			}
		}
	}
	// Unknown historical retirement times get a fresh observation/grace period.
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO catalog_retirements
		SELECT g.id,? FROM catalog_generations g
		WHERE NOT EXISTS (SELECT 1 FROM current_catalog c WHERE c.generation_id=g.id)`, unixNano(now)); err != nil {
		return fmt.Errorf("initialize catalog retirement observations: %w", err)
	}
	for _, statement := range []string{
		`CREATE TRIGGER IF NOT EXISTS catalog_retention_activate AFTER INSERT ON current_catalog BEGIN
			DELETE FROM catalog_retirements WHERE generation_id=NEW.generation_id;
		END`,
		`CREATE TRIGGER IF NOT EXISTS catalog_retention_replace AFTER UPDATE OF generation_id ON current_catalog
		WHEN OLD.generation_id<>NEW.generation_id BEGIN
			INSERT INTO catalog_retirements(generation_id,retired_at) VALUES (OLD.generation_id,` + catalogRetirementClockSQL + `)
				ON CONFLICT(generation_id) DO UPDATE SET retired_at=excluded.retired_at;
			DELETE FROM catalog_retirements WHERE generation_id=NEW.generation_id;
		END`,
		`CREATE TRIGGER IF NOT EXISTS catalog_retention_remove AFTER DELETE ON current_catalog BEGIN
			INSERT INTO catalog_retirements(generation_id,retired_at)
				SELECT OLD.generation_id,` + catalogRetirementClockSQL + `
				WHERE EXISTS (SELECT 1 FROM catalog_generations WHERE id=OLD.generation_id)
				ON CONFLICT(generation_id) DO UPDATE SET retired_at=excluded.retired_at;
		END`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("install legacy-compatible catalog retention: %w", err)
		}
	}
	return nil
}

func activateCatalogGeneration(ctx context.Context, tx *sql.Tx, meta store.CatalogGenerationMeta) error {
	var previous string
	err := tx.QueryRowContext(ctx, "SELECT generation_id FROM current_catalog WHERE workspace_id=?", meta.WorkspaceID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO current_catalog(workspace_id, generation_id) VALUES (?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET generation_id=excluded.generation_id`, meta.WorkspaceID, meta.ID); err != nil {
		return err
	}
	// New publishers retain their explicit observation clock; triggers above
	// ensure old publishers also record retirement and reactivation safely.
	if previous != "" && previous != meta.ID {
		if _, err := tx.ExecContext(ctx, "UPDATE catalog_retirements SET retired_at=? WHERE generation_id=?", unixNano(meta.CreatedAt), previous); err != nil {
			return fmt.Errorf("record catalog replacement time: %w", err)
		}
	}
	return nil
}

func (r *Repository) DeleteRetiredCatalogGenerations(ctx context.Context, workspaceID string, before, now time.Time, limit uint64) (uint64, error) {
	if limit == 0 {
		return 0, nil
	}
	result, err := r.db.ExecContext(ctx, `DELETE FROM catalog_generations WHERE id IN (
		SELECT g.id FROM catalog_generations g JOIN catalog_retirements r ON r.generation_id=g.id
		WHERE g.workspace_id=? AND r.retired_at<=?
		  AND NOT EXISTS (SELECT 1 FROM current_catalog c WHERE c.generation_id=g.id)
		  AND NOT EXISTS (SELECT 1 FROM result_sets s WHERE s.generation_id=g.id AND s.expires_at>?)
		ORDER BY r.retired_at, g.id LIMIT ?
	)`, workspaceID, unixNano(before), unixNano(now), limit)
	if err != nil {
		return 0, fmt.Errorf("delete retired catalog generations: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count deleted catalog generations: %w", err)
	}
	return uint64(count), nil
}
