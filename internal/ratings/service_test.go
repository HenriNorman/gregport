package ratings

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeRepo struct {
	ticket       Ticket
	loadErr      error
	insertErr    error
	inserted     []Rating
	ticketID     uuid.UUID
	customerID   uuid.UUID
	insertOrg    uuid.UUID
	average      float64
	averageCount int64
}

func (f *fakeRepo) LoadRatableTicket(_ context.Context, ticketID, customerID uuid.UUID) (Ticket, error) {
	f.ticketID, f.customerID = ticketID, customerID
	if f.loadErr != nil {
		return Ticket{}, f.loadErr
	}
	if f.ticket.ID == uuid.Nil {
		return Ticket{}, ErrTicketNotFound
	}
	if f.ticket.CustomerID != customerID || f.ticket.ID != ticketID {
		return Ticket{}, ErrTicketNotFound
	}
	return f.ticket, nil
}

func (f *fakeRepo) InsertRating(_ context.Context, orgID, ticketID, customerID uuid.UUID, score int, comment string) (Rating, error) {
	f.insertOrg = orgID
	if f.insertErr != nil {
		return Rating{}, f.insertErr
	}
	r := Rating{
		ID:         uuid.New(),
		OrgID:      orgID,
		TicketID:   ticketID,
		CustomerID: customerID,
		Score:      score,
		Comment:    comment,
		CreatedAt:  time.Now().UTC(),
	}
	f.inserted = append(f.inserted, r)
	return r, nil
}

func (f *fakeRepo) AverageScore(context.Context, uuid.UUID) (float64, int64, error) {
	return f.average, f.averageCount, nil
}

type fakeInvalidator struct {
	calls []uuid.UUID
	err   error
}

func (f *fakeInvalidator) InvalidateCounts(_ context.Context, orgID uuid.UUID) error {
	f.calls = append(f.calls, orgID)
	return f.err
}

func serviceFixture(t *testing.T) (*Service, *fakeRepo, *fakeInvalidator, *TokenManager, Ticket) {
	t.Helper()
	ticket := Ticket{
		ID:         uuid.New(),
		OrgID:      uuid.New(),
		CustomerID: uuid.New(),
		Number:     1042,
		Status:     "solved",
	}
	repo := &fakeRepo{ticket: ticket}
	tokens := NewTokenManager(strings.Repeat("s", 32), time.Hour)
	inv := &fakeInvalidator{}
	svc := NewService(repo, tokens)
	svc.SetCountsInvalidator(inv)
	return svc, repo, inv, tokens, ticket
}

func tokenFor(t *testing.T, m *TokenManager, ticket Ticket) string {
	t.Helper()
	token, err := m.Sign(ticket.ID, ticket.CustomerID)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return token
}

func TestServiceSubmit(t *testing.T) {
	svc, repo, inv, tokens, ticket := serviceFixture(t)

	got, err := svc.Submit(context.Background(), CreateRequest{
		Token:   tokenFor(t, tokens, ticket),
		Score:   5,
		Comment: "  Fast and helpful  ",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got.TicketNumber != ticket.Number || got.Score != 5 || got.Comment != "Fast and helpful" {
		t.Fatalf("response: %+v", got)
	}
	if len(repo.inserted) != 1 || repo.inserted[0].Score != 5 {
		t.Fatalf("inserted: %+v", repo.inserted)
	}
	if repo.insertOrg != ticket.OrgID {
		t.Fatalf("org=%s, want %s", repo.insertOrg, ticket.OrgID)
	}
	if repo.customerID != ticket.CustomerID {
		t.Fatalf("customer=%s, want %s", repo.customerID, ticket.CustomerID)
	}
	if len(inv.calls) != 1 || inv.calls[0] != ticket.OrgID {
		t.Fatalf("invalidator calls: %+v", inv.calls)
	}
}

func TestServiceSubmitRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		req  CreateRequest
		want error
	}{
		{name: "score zero", req: CreateRequest{Score: 0}, want: ErrInvalidScore},
		{name: "score six", req: CreateRequest{Score: 6}, want: ErrInvalidScore},
		{name: "negative score", req: CreateRequest{Score: -1}, want: ErrInvalidScore},
		{name: "comment too long", req: CreateRequest{Score: 5, Comment: strings.Repeat("x", MaxCommentLength+1)}, want: ErrInvalidComment},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo, _, _, _ := serviceFixture(t)
			if _, err := svc.Submit(context.Background(), tt.req); !errors.Is(err, tt.want) {
				t.Fatalf("err=%v, want %v", err, tt.want)
			}
			if len(repo.inserted) != 0 {
				t.Fatalf("invalid input was persisted: %+v", repo.inserted)
			}
		})
	}
}

func TestServiceSubmitRejectsTamperedToken(t *testing.T) {
	svc, repo, inv, tokens, ticket := serviceFixture(t)
	token := tokenFor(t, tokens, ticket)

	for _, bad := range []string{
		token + "x",
		"garbage",
		strings.ReplaceAll(token, ".", ""),
	} {
		if _, err := svc.Submit(context.Background(), CreateRequest{Token: bad, Score: 5}); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("token %q: err=%v, want ErrInvalidToken", bad, err)
		}
	}
	if len(repo.inserted) != 0 || len(inv.calls) != 0 {
		t.Fatalf("tampered token had effects: %+v / %+v", repo.inserted, inv.calls)
	}
}

func TestServiceSubmitRejectsExpiredToken(t *testing.T) {
	svc, repo, _, tokens, ticket := serviceFixture(t)
	base := time.Now()
	tokens.now = func() time.Time { return base }
	token := tokenFor(t, tokens, ticket)
	tokens.now = func() time.Time { return base.Add(2 * time.Hour) }

	if _, err := svc.Submit(context.Background(), CreateRequest{Token: token, Score: 5}); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("err=%v, want ErrExpiredToken", err)
	}
	if len(repo.inserted) != 0 {
		t.Fatal("expired token was persisted")
	}
}

func TestServiceSubmitWithoutTokenManager(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo, nil)

	if _, err := svc.Submit(context.Background(), CreateRequest{Token: "x", Score: 5}); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err=%v, want ErrInvalidToken", err)
	}
}

func TestServiceSubmitRejectsUnknownOrForeignTicket(t *testing.T) {
	tests := []struct {
		name    string
		loadErr error
		ticket  Ticket
	}{
		{name: "missing ticket", loadErr: ErrTicketNotFound},
		{name: "foreign customer", ticket: Ticket{ID: uuid.New(), CustomerID: uuid.New()}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo, inv, tokens, ticket := serviceFixture(t)
			repo.loadErr = tt.loadErr
			if tt.ticket.ID != uuid.Nil {
				repo.ticket = tt.ticket
			}
			// Sign for the expected ticket so the token itself is valid; the
			// repository is what rejects the mismatch.
			token := tokenFor(t, tokens, ticket)

			if _, err := svc.Submit(context.Background(), CreateRequest{Token: token, Score: 4}); !errors.Is(err, ErrRatingNotAvailable) {
				t.Fatalf("err=%v, want ErrRatingNotAvailable", err)
			}
			if len(repo.inserted) != 0 || len(inv.calls) != 0 {
				t.Fatalf("rejected ticket had effects: %+v / %+v", repo.inserted, inv.calls)
			}
		})
	}
}

func TestServiceSubmitAlreadyRated(t *testing.T) {
	svc, repo, inv, tokens, ticket := serviceFixture(t)
	repo.insertErr = ErrAlreadyRated

	if _, err := svc.Submit(context.Background(), CreateRequest{Token: tokenFor(t, tokens, ticket), Score: 5}); !errors.Is(err, ErrAlreadyRated) {
		t.Fatalf("err=%v, want ErrAlreadyRated", err)
	}
	if len(inv.calls) != 0 {
		t.Fatalf("invalidator called for a rejected rating: %+v", inv.calls)
	}
}

func TestServiceSubmitInvalidationIsBestEffort(t *testing.T) {
	svc, _, inv, tokens, ticket := serviceFixture(t)
	inv.err = errors.New("redis down")

	if _, err := svc.Submit(context.Background(), CreateRequest{Token: tokenFor(t, tokens, ticket), Score: 4}); err != nil {
		t.Fatalf("submit must ignore cache invalidation failure: %v", err)
	}
	if len(inv.calls) != 1 {
		t.Fatalf("invalidator calls: %+v", inv.calls)
	}
}
