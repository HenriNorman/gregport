package tickets

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// recordingCountsInvalidator counts dashboard invalidation calls and can be
// made to fail, so tests can prove a cache outage never fails a mutation.
type recordingCountsInvalidator struct {
	mu    sync.Mutex
	calls int
	orgs  []uuid.UUID
	err   error
}

func (r *recordingCountsInvalidator) InvalidateCounts(_ context.Context, orgID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls++
	r.orgs = append(r.orgs, orgID)
	return r.err
}

func (r *recordingCountsInvalidator) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func invalidationFixture(t *testing.T) (*Service, *Repository, *recordingCountsInvalidator, uuid.UUID, uuid.UUID, int64) {
	t.Helper()

	repo := NewRepository(testPool(t))
	svc := NewService(repo, NewInvitationTokenManager(7*24*time.Hour))
	invalidator := &recordingCountsInvalidator{}
	svc.SetCountsInvalidator(invalidator)

	orgA, orgB, number := setupTwoOrgs(t, repo)
	return svc, repo, invalidator, orgA, orgB, number
}

func ticketPrincipal(orgID uuid.UUID) requestctx.Principal {
	return requestctx.Principal{
		Kind:    requestctx.KindUser,
		ActorID: uuid.New(),
		OrgID:   orgID,
		Role:    "owner",
	}
}

func TestTicketMutationsInvalidateDashboardCounts(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		svc, _, invalidator, orgA, _, _ := invalidationFixture(t)

		if _, err := svc.CreateTicket(context.Background(), ticketPrincipal(orgA), CreateTicketRequest{
			RequesterEmail: "new@test.local",
			Subject:        "New ticket",
			Body:           "body",
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
		if invalidator.count() != 1 {
			t.Fatalf("invalidation calls: %d, want 1", invalidator.count())
		}
		if invalidator.orgs[0] != orgA {
			t.Fatalf("invalidated org %s, want %s", invalidator.orgs[0], orgA)
		}
	})

	t.Run("update", func(t *testing.T) {
		svc, repo, invalidator, orgA, _, number := invalidationFixture(t)
		ctx := context.Background()

		tk, err := repo.GetTicket(ctx, orgA, number)
		if err != nil {
			t.Fatalf("get ticket: %v", err)
		}
		subject := "Updated subject"
		if _, err := svc.UpdateTicket(ctx, ticketPrincipal(orgA), number, tk.Version, UpdateTicketRequest{
			Subject: &subject,
		}); err != nil {
			t.Fatalf("update: %v", err)
		}
		if invalidator.count() != 1 {
			t.Fatalf("invalidation calls: %d, want 1", invalidator.count())
		}
	})

	t.Run("delete", func(t *testing.T) {
		svc, _, invalidator, orgA, _, number := invalidationFixture(t)

		if err := svc.DeleteTicket(context.Background(), ticketPrincipal(orgA), number); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if invalidator.count() != 1 {
			t.Fatalf("invalidation calls: %d, want 1", invalidator.count())
		}
	})

	t.Run("assign", func(t *testing.T) {
		svc, repo, invalidator, orgA, _, number := invalidationFixture(t)
		assignee := seedMembership(t, repo, orgA)
		assigneeID := assignee.String()

		if _, err := svc.AssignTicket(context.Background(), ticketPrincipal(orgA), number, AssignTicketRequest{
			AssigneeID: &assigneeID,
		}); err != nil {
			t.Fatalf("assign: %v", err)
		}
		if invalidator.count() != 1 {
			t.Fatalf("invalidation calls: %d, want 1", invalidator.count())
		}
	})

	t.Run("merge", func(t *testing.T) {
		svc, repo, invalidator, orgA, _, sourceNumber := invalidationFixture(t)
		ctx := context.Background()

		target, _, err := repo.CreateTicket(ctx, CreateInput{
			OrgID:          orgA,
			Actor:          Actor{Type: "system"},
			RequesterEmail: "req@test.local",
			RequesterName:  "Req",
			Subject:        "Merge target",
			Body:           "body",
			Priority:       "normal",
		})
		if err != nil {
			t.Fatalf("create target: %v", err)
		}

		if _, _, err := svc.MergeTickets(ctx, ticketPrincipal(orgA), sourceNumber, target.Number); err != nil {
			t.Fatalf("merge: %v", err)
		}
		if invalidator.count() != 1 {
			t.Fatalf("invalidation calls: %d, want 1", invalidator.count())
		}
	})
}

func TestFailedTicketMutationsDoNotInvalidate(t *testing.T) {
	t.Run("invalid create", func(t *testing.T) {
		svc, _, invalidator, orgA, _, _ := invalidationFixture(t)

		_, err := svc.CreateTicket(context.Background(), ticketPrincipal(orgA), CreateTicketRequest{
			RequesterEmail: "new@test.local",
			Subject:        "",
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput, got %v", err)
		}
		if invalidator.count() != 0 {
			t.Fatalf("invalidation calls: %d, want 0", invalidator.count())
		}
	})

	t.Run("version mismatch", func(t *testing.T) {
		svc, _, invalidator, orgA, _, number := invalidationFixture(t)
		subject := "Stale write"

		_, err := svc.UpdateTicket(context.Background(), ticketPrincipal(orgA), number, 999, UpdateTicketRequest{
			Subject: &subject,
		})
		if !errors.Is(err, ErrVersionMismatch) {
			t.Fatalf("expected ErrVersionMismatch, got %v", err)
		}
		if invalidator.count() != 0 {
			t.Fatalf("invalidation calls: %d, want 0", invalidator.count())
		}
	})

	t.Run("forbidden delete", func(t *testing.T) {
		svc, _, invalidator, orgA, _, number := invalidationFixture(t)
		principal := ticketPrincipal(orgA)
		principal.Role = "agent"

		if err := svc.DeleteTicket(context.Background(), principal, number); !errors.Is(err, ErrForbidden) {
			t.Fatalf("expected ErrForbidden, got %v", err)
		}
		if invalidator.count() != 0 {
			t.Fatalf("invalidation calls: %d, want 0", invalidator.count())
		}
	})

	t.Run("assignee is not an agent", func(t *testing.T) {
		svc, _, invalidator, orgA, _, number := invalidationFixture(t)
		stranger := uuid.NewString()

		_, err := svc.AssignTicket(context.Background(), ticketPrincipal(orgA), number, AssignTicketRequest{
			AssigneeID: &stranger,
		})
		if !errors.Is(err, ErrAssigneeNotAgent) {
			t.Fatalf("expected ErrAssigneeNotAgent, got %v", err)
		}
		if invalidator.count() != 0 {
			t.Fatalf("invalidation calls: %d, want 0", invalidator.count())
		}
	})

	t.Run("merge into self", func(t *testing.T) {
		svc, _, invalidator, orgA, _, number := invalidationFixture(t)

		_, _, err := svc.MergeTickets(context.Background(), ticketPrincipal(orgA), number, number)
		if !errors.Is(err, ErrMergeSelf) {
			t.Fatalf("expected ErrMergeSelf, got %v", err)
		}
		if invalidator.count() != 0 {
			t.Fatalf("invalidation calls: %d, want 0", invalidator.count())
		}
	})

	t.Run("mutation in another org", func(t *testing.T) {
		svc, _, invalidator, _, orgB, number := invalidationFixture(t)

		if err := svc.DeleteTicket(context.Background(), ticketPrincipal(orgB), number); !errors.Is(err, ErrTicketNotFound) {
			t.Fatalf("expected ErrTicketNotFound, got %v", err)
		}
		if invalidator.count() != 0 {
			t.Fatalf("invalidation calls: %d, want 0", invalidator.count())
		}
	})
}

func TestCountsInvalidationFailureDoesNotFailMutation(t *testing.T) {
	svc, _, invalidator, orgA, _, number := invalidationFixture(t)
	invalidator.err = errors.New("redis down")

	if err := svc.DeleteTicket(context.Background(), ticketPrincipal(orgA), number); err != nil {
		t.Fatalf("delete must succeed when cache invalidation fails: %v", err)
	}
	if invalidator.count() != 1 {
		t.Fatalf("invalidation calls: %d, want 1", invalidator.count())
	}
}

func TestServiceWithoutCountsInvalidatorStillWorks(t *testing.T) {
	repo := NewRepository(testPool(t))
	svc := NewService(repo, NewInvitationTokenManager(7*24*time.Hour))
	orgA, _, _ := setupTwoOrgs(t, repo)

	if _, err := svc.CreateTicket(context.Background(), ticketPrincipal(orgA), CreateTicketRequest{
		RequesterEmail: "no-cache@test.local",
		Subject:        "No cache",
		Body:           "body",
	}); err != nil {
		t.Fatalf("create without invalidator: %v", err)
	}
}
