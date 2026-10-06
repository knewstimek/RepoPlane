package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"repoplane/internal/store"
)

func migrateV4(ctx context.Context, tx *sql.Tx, now time.Time) error {
	for _, statement := range []string{
		"ALTER TABLE catalog_generations ADD COLUMN retired_at INTEGER",
		"CREATE INDEX catalog_generations_retired ON catalog_generations(workspace_id, retired_at, id)",
		"CREATE INDEX result_sets_generation_expiry ON result_sets(generation_id, expires_at)",
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply sqlite migration 4: %w", err)
		}
	}
	// Earlier schemas did not record retirement. Start a fresh grace period
	// when an inactive generation is first observed; creation time cannot prove
	// how recently a long-lived current generation was replaced.
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_generations SET retired_at=?
		WHERE NOT EXISTS (SELECT 1 FROM current_catalog c WHERE c.generation_id=catalog_generations.id)`, unixNano(now)); err != nil {
		return fmt.Errorf("initialize catalog retirement observations: %w", err)
	}
	return nil
}

func activateCatalogGeneration(ctx context.Context, tx *sql.Tx, meta store.CatalogGenerationMeta) error {
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_generations SET retired_at=?
		WHERE id=(SELECT generation_id FROM current_catalog WHERE workspace_id=?) AND id<>?`,
		unixNano(meta.CreatedAt), meta.WorkspaceID, meta.ID); err != nil {
		return fmt.Errorf("retire previous catalog generation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE catalog_generations SET retired_at=NULL WHERE id=?", meta.ID); err != nil {
		return fmt.Errorf("clear reactivated catalog retirement: %w", err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO current_catalog(workspace_id, generation_id) VALUES (?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET generation_id=excluded.generation_id`, meta.WorkspaceID, meta.ID)
	return err
}

func (r *Repository) DeleteRetiredCatalogGenerations(ctx context.Context, workspaceID string, before, now time.Time, limit uint64) (uint64, error) {
	if limit == 0 {
		return 0, nil
	}
	result, err := r.db.ExecContext(ctx, `DELETE FROM catalog_generations WHERE id IN (
		SELECT g.id FROM catalog_generations g
		WHERE g.workspace_id=? AND g.retired_at<=?
		  AND NOT EXISTS (SELECT 1 FROM current_catalog c WHERE c.generation_id=g.id)
		  AND NOT EXISTS (SELECT 1 FROM result_sets s WHERE s.generation_id=g.id AND s.expires_at>?)
		ORDER BY g.retired_at, g.id LIMIT ?
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
