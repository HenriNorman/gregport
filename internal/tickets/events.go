package tickets

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func Record(
	ctx context.Context,
	tx pgx.Tx,
	orgID, ticketID uuid.UUID,
	actor Actor,
	eventType string,
	data map[string]any,
) error {
	if data == nil {
		data = map[string]any{}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal event data: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO ticket_events (org_id, ticket_id, actor_type, actor_id, event_type, data)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		orgID, ticketID, actor.Type, actor.ID, eventType, raw,
	)
	if err != nil {
		return fmt.Errorf("insert event %s: %w", eventType, err)
	}
	return nil
}
