package realtime

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository reads the replay cursor from ticket_events and checks ticket
// ownership for typing.
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Replay returns events for one org with a seq greater than afterSeq, oldest
// first. It joins the ticket only for its human-facing number.
func (r *Repository) Replay(ctx context.Context, orgID uuid.UUID, afterSeq int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := r.pool.Query(ctx,
		`SELECT te.seq, te.id, te.event_type, te.ticket_id,
		        COALESCE(t.number, 0), te.actor_id, te.data, te.created_at
		 FROM ticket_events te
		 LEFT JOIN tickets t ON t.id = te.ticket_id AND t.org_id = te.org_id
		 WHERE te.org_id = $1 AND te.seq > $2
		 ORDER BY te.seq
		 LIMIT $3`,
		orgID, afterSeq, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("replay ticket events: %w", err)
	}
	defer rows.Close()

	events := make([]Event, 0, limit)
	for rows.Next() {
		var (
			ev     Event
			data   []byte
			actor  *uuid.UUID
			number int64
		)
		if err := rows.Scan(
			&ev.Seq, &ev.ID, &ev.Type, &ev.TicketID,
			&number, &actor, &data, &ev.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan replay event: %w", err)
		}
		ev.TicketNumber = number
		ev.ActorID = actor
		ev.Data = SafeData(ev.Type, data)
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate replay events: %w", err)
	}
	return events, nil
}

// TicketExists is scoped to the org, so a ticket number from another tenant is
// indistinguishable from a missing ticket.
func (r *Repository) TicketExists(ctx context.Context, orgID uuid.UUID, number int64) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM tickets
		   WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL
		 )`,
		orgID, number,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check ticket: %w", err)
	}
	return exists, nil
}
