package customers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/HenriNorman/gregport/internal/platform/audit"
	"github.com/HenriNorman/gregport/internal/platform/database"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// Store is the persistence contract the service uses.
type Store interface {
	Erase(ctx context.Context, orgID, id uuid.UUID, pseudonym string, actor requestctx.Principal) (Customer, error)
}

// Repository persists customer erasure.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a PostgreSQL-backed customer repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Erase replaces the customer's identifying fields and soft-deletes the row.
// Ticket, comment and event history is left in place. The pseudonym is stable,
// so erasing an already-erased customer is a no-op that returns the same row.
func (r *Repository) Erase(ctx context.Context, orgID, id uuid.UUID, pseudonym string, actor requestctx.Principal) (Customer, error) {
	var out Customer
	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var existing Customer
		var name, externalID sql.NullString
		var deletedAt sql.NullTime
		err := tx.QueryRow(ctx,
			`SELECT id, org_id, email, name, external_id, deleted_at
			 FROM customers
			 WHERE org_id = $1 AND id = $2
			 FOR UPDATE`,
			orgID, id,
		).Scan(&existing.ID, &existing.OrgID, &existing.Email, &name, &externalID, &deletedAt)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrCustomerNotFound
			}
			return fmt.Errorf("lock customer: %w", err)
		}
		if name.Valid {
			existing.Name = &name.String
		}
		if externalID.Valid {
			existing.ExternalID = &externalID.String
		}
		if deletedAt.Valid {
			existing.DeletedAt = &deletedAt.Time
			// Already erased: keep the existing pseudonym and do not write a
			// second audit row for the same action.
			out = existing
			return nil
		}

		if _, err := tx.Exec(ctx,
			`UPDATE customers
			 SET email = $3, name = NULL, external_id = NULL,
			     deleted_at = now(), updated_at = now()
			 WHERE org_id = $1 AND id = $2`,
			orgID, id, pseudonym,
		); err != nil {
			return fmt.Errorf("erase customer: %w", err)
		}
		if err := audit.Record(ctx, tx, orgID, actor, "customer.erased", "customer", id, nil); err != nil {
			return err
		}

		erasedAt := time.Now().UTC()
		out = Customer{ID: id, OrgID: orgID, Email: pseudonym, DeletedAt: &erasedAt}
		return nil
	})
	if err != nil {
		return Customer{}, err
	}
	return out, nil
}
