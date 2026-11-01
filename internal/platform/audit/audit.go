package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

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
