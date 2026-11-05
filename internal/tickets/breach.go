// Package tickets breach checking scans for tickets past their SLA due dates
// and records sla.breached ticket events so the outbox dispatcher can fan them
// out to email consumers.
package tickets

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/HenriNorman/gregport/internal/platform/database"
)

// BreachCandidate is a ticket whose SLA deadline has passed without the
// corresponding response or resolution.
type BreachCandidate struct {
	ID      uuid.UUID
	OrgID   uuid.UUID
	Number  int64
	Subject string

	FirstResponseDueAt *time.Time
	ResolutionDueAt    *time.Time
	FirstResponseAt    *time.Time
	ResolvedAt         *time.Time
}

// BreachType identifies which SLA deadline was breached.
type BreachType string

const (
	BreachFirstResponse BreachType = "first_response"
	BreachResolution    BreachType = "resolution"
)

// BreachChecker scans for tickets past their SLA due dates, marks the breach
// columns, and records an sla.breached ticket event for each.
//
// It is safe to run concurrently: candidate rows are locked with
// FOR UPDATE SKIP LOCKED so multiple workers never process the same breach.
type BreachChecker struct {
	repo *Repository
}

// NewBreachChecker builds a checker backed by repo.
func NewBreachChecker(repo *Repository) *BreachChecker {
	return &BreachChecker{repo: repo}
}

// CheckBreaches claims up to batch tickets past their SLA due dates, marks the
// breach columns, and records one sla.breached event per ticket. It returns the
// number of tickets newly marked as breached.
//
// A ticket is considered breached when its due date has passed, the matching
// breach column is still NULL, and the ticket has not yet been responded to
// (first response) or resolved (resolution). Soft-deleted tickets are skipped.
func (c *BreachChecker) CheckBreaches(ctx context.Context, batch int) (int, error) {
	if batch <= 0 {
		batch = 100
	}

	var total int
	err := database.WithTx(ctx, c.repo.pool, func(tx pgx.Tx) error {
		candidates, err := c.claimBreaches(ctx, tx, batch)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}

		for _, cand := range candidates {
			breaches := cand.breachTypes()
			if len(breaches) == 0 {
				// Race: another worker already marked this breach.
				continue
			}

			if err := c.markBreach(ctx, tx, cand.ID, breaches); err != nil {
				return fmt.Errorf("mark breach for ticket %d: %w", cand.Number, err)
			}

			data := map[string]any{
				"number":                cand.Number,
				"breach_types":          breachTypesToStrings(breaches),
				"first_response_due_at": cand.FirstResponseDueAt,
				"resolution_due_at":     cand.ResolutionDueAt,
			}
			if err := Record(ctx, tx, cand.OrgID, cand.ID, Actor{Type: "system", ID: nil}, "sla.breached", data); err != nil {
				return fmt.Errorf("record breach event for ticket %d: %w", cand.Number, err)
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

// claimBreaches locks and returns tickets past their SLA due dates that have
// not yet been marked as breached.
func (c *BreachChecker) claimBreaches(ctx context.Context, tx pgx.Tx, batch int) ([]BreachCandidate, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, org_id, number, subject,
		         first_response_due_at, resolution_due_at,
		         first_response_at, resolved_at
		 FROM tickets
		 WHERE deleted_at IS NULL
		   AND (
		         (first_response_due_at IS NOT NULL
		          AND first_response_breached_at IS NULL
		          AND first_response_at IS NULL
		          AND first_response_due_at <= now())
		       OR
		         (resolution_due_at IS NOT NULL
		          AND resolution_breached_at IS NULL
		          AND resolved_at IS NULL
		          AND resolution_due_at <= now())
		   )
		 ORDER BY LEAST(
		           COALESCE(first_response_due_at, resolution_due_at),
		           COALESCE(resolution_due_at, first_response_due_at)
		         ), id
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED`,
		batch,
	)
	if err != nil {
		return nil, fmt.Errorf("claim breached tickets: %w", err)
	}
	defer rows.Close()

	var out []BreachCandidate
	for rows.Next() {
		var cand BreachCandidate
		if err := rows.Scan(
			&cand.ID, &cand.OrgID, &cand.Number, &cand.Subject,
			&cand.FirstResponseDueAt, &cand.ResolutionDueAt,
			&cand.FirstResponseAt, &cand.ResolvedAt,
		); err != nil {
			return nil, fmt.Errorf("scan breach candidate: %w", err)
		}
		out = append(out, cand)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate breach candidates: %w", err)
	}
	return out, nil
}

// breachTypes returns the deadline types that are newly breached for this
// candidate. The caller must hold a lock on the ticket row.
func (c BreachCandidate) breachTypes() []BreachType {
	now := time.Now()
	var out []BreachType

	if c.FirstResponseDueAt != nil && !c.FirstResponseDueAt.After(now) && c.FirstResponseAt == nil {
		out = append(out, BreachFirstResponse)
	}
	if c.ResolutionDueAt != nil && !c.ResolutionDueAt.After(now) && c.ResolvedAt == nil {
		out = append(out, BreachResolution)
	}
	return out
}

func (c *BreachChecker) markBreach(ctx context.Context, tx pgx.Tx, ticketID uuid.UUID, breaches []BreachType) error {
	var firstResp, resolution bool
	for _, b := range breaches {
		switch b {
		case BreachFirstResponse:
			firstResp = true
		case BreachResolution:
			resolution = true
		}
	}

	sets := []string{"updated_at = now()"}
	if firstResp {
		sets = append(sets, "first_response_breached_at = now()")
	}
	if resolution {
		sets = append(sets, "resolution_breached_at = now()")
	}

	query := `UPDATE tickets SET ` + strings.Join(sets, ", ") + ` WHERE id = $1 AND deleted_at IS NULL`
	if _, err := tx.Exec(ctx, query, ticketID); err != nil {
		return fmt.Errorf("update ticket breach: %w", err)
	}
	return nil
}

func breachTypesToStrings(breaches []BreachType) []string {
	out := make([]string, 0, len(breaches))
	for _, b := range breaches {
		out = append(out, string(b))
	}
	return out
}
