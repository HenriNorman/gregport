package customers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, "postgres://postgres:Henri123@localhost:5432/gregport?sslmode=disable")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestEraseAnonymizesAndKeepsHistory(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)

	orgID := uuid.New()
	otherOrgID := uuid.New()
	customerID := uuid.New()
	otherCustomerID := uuid.New()
	userID := uuid.New()
	ticketID := uuid.New()
	commentID := uuid.New()

	for _, org := range []struct {
		id   uuid.UUID
		slug string
	}{
		{orgID, "erase-" + orgID.String()[:8]},
		{otherOrgID, "erase-" + otherOrgID.String()[:8]},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO organizations (id, name, slug) VALUES ($1, 'Erase Test', $2)`,
			org.id, org.slug,
		); err != nil {
			t.Fatalf("seed org: %v", err)
		}
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, name) VALUES ($1, $2, 'x', 'Erase User')`,
		userID, "erase-"+userID.String()[:8]+"@test.local",
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'owner')`,
		orgID, userID,
	); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	originalEmail := "person-" + customerID.String()[:8] + "@test.local"
	if _, err := pool.Exec(ctx,
		`INSERT INTO customers (id, org_id, email, name, external_id)
		 VALUES ($1, $2, $3, 'Real Name', 'rider-42')`,
		customerID, orgID, originalEmail,
	); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO customers (id, org_id, email, name)
		 VALUES ($1, $2, $3, 'Other Person')`,
		otherCustomerID, otherOrgID, "other-"+otherCustomerID.String()[:8]+"@test.local",
	); err != nil {
		t.Fatalf("seed other customer: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tickets (id, org_id, number, requester_id, subject, status, priority, channel)
		 VALUES ($1, $2, 1, $3, 'History', 'open', 'normal', 'api')`,
		ticketID, orgID, customerID,
	); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO comments (id, org_id, ticket_id, author_type, author_customer_id, body)
		 VALUES ($1, $2, $3, 'customer', $4, 'hello')`,
		commentID, orgID, ticketID, customerID,
	); err != nil {
		t.Fatalf("seed comment: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM audit_logs WHERE org_id = ANY($1)`, []uuid.UUID{orgID, otherOrgID})
		_, _ = pool.Exec(bg, `DELETE FROM comments WHERE org_id = ANY($1)`, []uuid.UUID{orgID, otherOrgID})
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = ANY($1)`, []uuid.UUID{orgID, otherOrgID})
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = ANY($1)`, []uuid.UUID{orgID, otherOrgID})
		_, _ = pool.Exec(bg, `DELETE FROM memberships WHERE org_id = ANY($1)`, []uuid.UUID{orgID, otherOrgID})
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = ANY($1)`, []uuid.UUID{orgID, otherOrgID})
	})

	actor := requestctx.Principal{Kind: requestctx.KindUser, ActorID: userID, OrgID: orgID, Role: "owner"}

	// A customer in another org is indistinguishable from a missing one.
	if _, err := repo.Erase(ctx, otherOrgID, customerID, AnonymizedEmail(otherOrgID, customerID), actor); !errors.Is(err, ErrCustomerNotFound) {
		t.Fatalf("foreign erase err=%v", err)
	}

	got, err := repo.Erase(ctx, orgID, customerID, AnonymizedEmail(orgID, customerID), actor)
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	want := AnonymizedEmail(orgID, customerID)
	if got.Email != want {
		t.Fatalf("email=%q want %q", got.Email, want)
	}

	var email string
	var name, externalID *string
	var deletedAt *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT email, name, external_id, deleted_at FROM customers WHERE id = $1`, customerID,
	).Scan(&email, &name, &externalID, &deletedAt); err != nil {
		t.Fatalf("read customer: %v", err)
	}
	if email != want {
		t.Fatalf("stored email=%q", email)
	}
	if name != nil || externalID != nil {
		t.Fatalf("identifying fields remain: name=%v external=%v", name, externalID)
	}
	if deletedAt == nil {
		t.Fatal("customer was not soft-deleted")
	}

	// Ticket and comment history survive.
	var ticketCount, commentCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE id = $1`, ticketID).Scan(&ticketCount); err != nil {
		t.Fatalf("count tickets: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comments WHERE id = $1`, commentID).Scan(&commentCount); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if ticketCount != 1 || commentCount != 1 {
		t.Fatalf("history lost: tickets=%d comments=%d", ticketCount, commentCount)
	}

	// Other org is untouched.
	var otherEmail string
	if err := pool.QueryRow(ctx, `SELECT email FROM customers WHERE id = $1`, otherCustomerID).Scan(&otherEmail); err != nil {
		t.Fatalf("read other customer: %v", err)
	}
	if otherEmail == want || otherEmail == "" {
		t.Fatalf("other org customer changed: %q", otherEmail)
	}

	// Repeating erasure is idempotent and does not duplicate the audit row.
	if _, err := repo.Erase(ctx, orgID, customerID, AnonymizedEmail(orgID, customerID), actor); err != nil {
		t.Fatalf("erase twice: %v", err)
	}
	var auditCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs WHERE org_id = $1 AND action = 'customer.erased'`, orgID,
	).Scan(&auditCount); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("audit rows=%d want 1", auditCount)
	}
}
