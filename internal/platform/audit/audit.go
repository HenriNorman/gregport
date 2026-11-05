package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// PurgeOldLogs deletes audit_logs older than before, limited to batch rows.
func PurgeOldLogs(ctx context.Context, pool *pgxpool.Pool, before time.Time, batch int) (int64, error) {
	if before.IsZero() {
		return 0, fmt.Errorf("purge time is required")
	}
	if batch <= 0 {
		batch = 100
	}
	tag, err := pool.Exec(ctx,
		`WITH to_delete AS (
		         SELECT id FROM audit_logs
		         WHERE created_at <= $1
		         LIMIT $2
		     )
		     DELETE FROM audit_logs
		     WHERE id IN (SELECT id FROM to_delete)`,
		before, batch,
	)
	if err != nil {
		return 0, fmt.Errorf("purge old audit logs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Record inserts an audit row inside the caller's transaction.
// actor.Kind and actor.ActorID come from the request principal.
func Record(
	ctx context.Context,
	tx pgx.Tx,
	orgID uuid.UUID,
	actor requestctx.Principal,
	action string,
	entityType string,
	entityID uuid.UUID,
	data map[string]any,
) error {
	if data == nil {
		data = map[string]any{}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal audit data: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO audit_logs (org_id, actor_type, actor_id, action, entity_type, entity_id, data)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		orgID, string(actor.Kind), actor.ActorID, action, entityType, entityID, raw,
	)
	if err != nil {
		return fmt.Errorf("insert audit %s: %w", action, err)
	}
	return nil
}
