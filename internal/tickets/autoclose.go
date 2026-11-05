// Package tickets provides ticket domain logic including auto-closing solved
// tickets once their SLA resolution window has passed.
package tickets

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/HenriNorman/gregport/internal/platform/database"
)

// AutoCloseCandidate is a solved ticket that has aged past the auto-close
// window and should be transitioned to closed.
type AutoCloseCandidate struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	Number     int64
	Subject    string
	ResolvedAt time.Time
}

// AutoCloseChecker scans solved tickets past the auto-close window, closes
// them (solved -> closed, setting closed_at) and records a ticket.closed
// outbox event for each.
//
// It is safe to run concurrently: candidate rows are locked with
// FOR UPDATE SKIP LOCKED so multiple workers never close the same ticket.
type AutoCloseChecker struct {
	repo       *Repository
	closeAfter time.Duration
}

// NewAutoCloseChecker builds a checker that closes tickets which have been
// solved for longer than closeAfter.
func NewAutoCloseChecker(repo *Repository, closeAfter time.Duration) *AutoCloseChecker {
	return &AutoCloseChecker{repo: repo, closeAfter: closeAfter}
}

// CheckAutoClose claims up to batch solved tickets past the close window,
// closes them and records a ticket.closed event per ticket. It returns the
// number of tickets closed.
func (c *AutoCloseChecker) CheckAutoClose(ctx context.Context, batch int) (int, error) {
	if batch <= 0 {
		batch = 100
	}
	cutoff := time.Now().UTC().Add(-c.closeAfter)

	var total int
	err := database.WithTx(ctx, c.repo.pool, func(tx pgx.Tx) error {
		candidates, err := c.claimAutoClose(ctx, tx, cutoff, batch)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}

		for _, cand := range candidates {
			if _, err := tx.Exec(ctx,
				`UPDATE tickets
				 SET status = 'closed', closed_at = now(), version = version + 1, updated_at = now()
				 WHERE id = $1 AND status = 'solved' AND closed_at IS NULL AND deleted_at IS NULL`,
				cand.ID,
			); err != nil {
				return fmt.Errorf("close ticket %d: %w", cand.Number, err)
			}

			if err := Record(ctx, tx, cand.OrgID, cand.ID, Actor{Type: "system", ID: nil},
				"ticket.closed", map[string]any{
					"number":      cand.Number,
					"resolved_at": cand.ResolvedAt,
				},
			); err != nil {
				return fmt.Errorf("record auto-close event for ticket %d: %w", cand.Number, err)
			}

			total++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// claimAutoClose locks solved tickets whose resolved_at is at or before the
// cutoff and which have not yet been closed.
func (c *AutoCloseChecker) claimAutoClose(ctx context.Context, tx pgx.Tx, cutoff time.Time, batch int) ([]AutoCloseCandidate, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, org_id, number, subject, resolved_at
		 FROM tickets
		 WHERE status = 'solved'
		   AND closed_at IS NULL
		   AND deleted_at IS NULL
		   AND resolved_at IS NOT NULL
		   AND resolved_at <= $1
		 ORDER BY resolved_at, id
		 LIMIT $2
		 FOR UPDATE SKIP LOCKED`,
		cutoff, batch,
	)
	if err != nil {
		return nil, fmt.Errorf("claim auto-close tickets: %w", err)
	}
	defer rows.Close()

	var out []AutoCloseCandidate
	for rows.Next() {
		var cand AutoCloseCandidate
		if err := rows.Scan(&cand.ID, &cand.OrgID, &cand.Number, &cand.Subject, &cand.ResolvedAt); err != nil {
			return nil, fmt.Errorf("scan auto-close candidate: %w", err)
		}
		out = append(out, cand)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate auto-close candidates: %w", err)
	}
	return out, nil
}
