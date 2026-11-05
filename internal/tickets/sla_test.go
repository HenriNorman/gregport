package tickets

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/cache"
	slapkg "github.com/HenriNorman/gregport/internal/sla"
)

// failingSLAStore proves that an SLA outage degrades to "no due dates" rather
// than failing ticket writes.
type failingSLAStore struct{}

func (failingSLAStore) List(context.Context, uuid.UUID) ([]slapkg.Policy, error) {
	return nil, errors.New("sla store down")
}

func slaFixture(t *testing.T) (*Service, *Repository, *slapkg.Repository, uuid.UUID) {
	t.Helper()

	pool := testPool(t)
	repo := NewRepository(pool)
	slaRepo := slapkg.NewRepository(pool)
	svc := NewService(repo, NewInvitationTokenManager(7*24*time.Hour))
	svc.SetSLAStore(slapkg.NewCachedStore(slaRepo, cache.NewMemory(), time.Minute))

	var orgID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO organizations (name, slug) VALUES ('SLA Tickets', $1) RETURNING id`,
		"sla-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM email_deliveries WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM ticket_events WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM comments WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM tickets WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM customers WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM sla_policies WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	return svc, repo, slaRepo, orgID
}

func seedPolicy(t *testing.T, repo *slapkg.Repository, orgID uuid.UUID, priority string, first, resolution int) slapkg.Policy {
	t.Helper()
	p, err := repo.Create(context.Background(), orgID, slapkg.Policy{
		Name:                 priority + " default",
		Priority:             priority,
		FirstResponseMinutes: first,
		ResolutionMinutes:    resolution,
	})
	if err != nil {
		t.Fatalf("seed policy %s: %v", priority, err)
	}
	return p
}

func within(t *testing.T, got time.Time, want time.Time, tolerance time.Duration, label string) {
	t.Helper()
	diff := got.Sub(want)
	if diff < 0 {
		diff = -diff
	}
	if diff > tolerance {
		t.Fatalf("%s = %s, want %s (±%s, off by %s)", label, got, want, tolerance, diff)
	}
}

func TestCreateTicketSetsSLADueDates(t *testing.T) {
	svc, repo, slaRepo, orgID := slaFixture(t)
	policy := seedPolicy(t, slaRepo, orgID, "urgent", 15, 120)

	tk, err := svc.CreateTicket(context.Background(), ticketPrincipal(orgID), CreateTicketRequest{
		Subject:        "Everything is on fire",
		Body:           "help",
		RequesterEmail: "fire@test.local",
		Priority:       "urgent",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if tk.SLAPolicyID == nil || *tk.SLAPolicyID != policy.ID {
		t.Fatalf("sla policy = %v, want %s", tk.SLAPolicyID, policy.ID)
	}
	if tk.FirstResponseDueAt == nil || tk.ResolutionDueAt == nil {
		t.Fatalf("due dates missing: fr=%v res=%v", tk.FirstResponseDueAt, tk.ResolutionDueAt)
	}
	within(t, *tk.FirstResponseDueAt, tk.CreatedAt.Add(15*time.Minute), 5*time.Second, "first response due")
	within(t, *tk.ResolutionDueAt, tk.CreatedAt.Add(120*time.Minute), 5*time.Second, "resolution due")

	// The row is persisted, not only returned.
	stored, err := repo.GetTicket(context.Background(), orgID, tk.Number)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.FirstResponseDueAt == nil || !stored.FirstResponseDueAt.Equal(*tk.FirstResponseDueAt) {
		t.Fatalf("stored first response due = %v", stored.FirstResponseDueAt)
	}
}

func TestCreateTicketWithoutPolicyHasNoDueDates(t *testing.T) {
	svc, _, _, orgID := slaFixture(t)

	tk, err := svc.CreateTicket(context.Background(), ticketPrincipal(orgID), CreateTicketRequest{
		Subject:        "No policy",
		Body:           "body",
		RequesterEmail: "none@test.local",
		Priority:       "low",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tk.SLAPolicyID != nil || tk.FirstResponseDueAt != nil || tk.ResolutionDueAt != nil {
		t.Fatalf("expected no SLA, got policy=%v fr=%v res=%v",
			tk.SLAPolicyID, tk.FirstResponseDueAt, tk.ResolutionDueAt)
	}
}

func TestPriorityChangeRecomputesSLADeadlinesFromCreation(t *testing.T) {
	svc, _, slaRepo, orgID := slaFixture(t)
	seedPolicy(t, slaRepo, orgID, "normal", 60, 480)
	urgent := seedPolicy(t, slaRepo, orgID, "urgent", 15, 120)

	tk, err := svc.CreateTicket(context.Background(), ticketPrincipal(orgID), CreateTicketRequest{
		Subject:        "Normal at first",
		Body:           "body",
		RequesterEmail: "upgrade@test.local",
		Priority:       "normal",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tk.FirstResponseDueAt == nil {
		t.Fatal("normal policy was not applied")
	}

	updated, err := svc.UpdateTicket(context.Background(), ticketPrincipal(orgID), tk.Number, tk.Version, UpdateTicketRequest{
		Priority: strPtr("urgent"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	if updated.SLAPolicyID == nil || *updated.SLAPolicyID != urgent.ID {
		t.Fatalf("sla policy = %v, want %s", updated.SLAPolicyID, urgent.ID)
	}
	within(t, *updated.FirstResponseDueAt, updated.CreatedAt.Add(15*time.Minute), 5*time.Second, "first response due")
	within(t, *updated.ResolutionDueAt, updated.CreatedAt.Add(120*time.Minute), 5*time.Second, "resolution due")
}

func TestPriorityChangeWithoutPolicyClearsSLADeadlines(t *testing.T) {
	svc, _, slaRepo, orgID := slaFixture(t)
	seedPolicy(t, slaRepo, orgID, "urgent", 15, 120)

	tk, err := svc.CreateTicket(context.Background(), ticketPrincipal(orgID), CreateTicketRequest{
		Subject:        "Urgent then low",
		Body:           "body",
		RequesterEmail: "downgrade@test.local",
		Priority:       "urgent",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updated, err := svc.UpdateTicket(context.Background(), ticketPrincipal(orgID), tk.Number, tk.Version, UpdateTicketRequest{
		Priority: strPtr("low"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.SLAPolicyID != nil || updated.FirstResponseDueAt != nil || updated.ResolutionDueAt != nil {
		t.Fatalf("expected cleared SLA, got policy=%v fr=%v res=%v",
			updated.SLAPolicyID, updated.FirstResponseDueAt, updated.ResolutionDueAt)
	}
}

func TestSamePriorityUpdateKeepsSLADeadlines(t *testing.T) {
	svc, repo, slaRepo, orgID := slaFixture(t)
	seedPolicy(t, slaRepo, orgID, "normal", 60, 480)

	tk, err := svc.CreateTicket(context.Background(), ticketPrincipal(orgID), CreateTicketRequest{
		Subject:        "Same priority",
		Body:           "body",
		RequesterEmail: "same@test.local",
		Priority:       "normal",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	sentinel := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := repo.pool.Exec(context.Background(),
		`UPDATE tickets SET first_response_due_at = $2, resolution_due_at = $3 WHERE id = $1`,
		tk.ID, sentinel, sentinel.Add(time.Hour),
	); err != nil {
		t.Fatalf("sentinel update: %v", err)
	}

	updated, err := svc.UpdateTicket(context.Background(), ticketPrincipal(orgID), tk.Number, tk.Version, UpdateTicketRequest{
		Priority: strPtr("normal"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.FirstResponseDueAt == nil || !updated.FirstResponseDueAt.Equal(sentinel) {
		t.Fatalf("same-priority update recomputed the due date: %v", updated.FirstResponseDueAt)
	}
}

func TestSLAStoreFailureDoesNotBlockTicketWrites(t *testing.T) {
	svc, _, _, orgID := slaFixture(t)
	svc.SetSLAStore(failingSLAStore{})

	tk, err := svc.CreateTicket(context.Background(), ticketPrincipal(orgID), CreateTicketRequest{
		Subject:        "SLA is down",
		Body:           "body",
		RequesterEmail: "outage@test.local",
		Priority:       "urgent",
	})
	if err != nil {
		t.Fatalf("create must succeed during SLA outage: %v", err)
	}
	if tk.FirstResponseDueAt != nil || tk.SLAPolicyID != nil {
		t.Fatal("expected no SLA when the policy store fails")
	}
}
