package dashboard

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type fakeStore struct {
	counts Counts
	err    error
	orgID  uuid.UUID
}

func (f *fakeStore) Counts(_ context.Context, orgID uuid.UUID) (Counts, error) {
	f.orgID = orgID
	if f.err != nil {
		return Counts{}, f.err
	}
	return f.counts, nil
}

func TestServicePassesTenantAndReturnsCounts(t *testing.T) {
	want := Counts{Total: 9, Open: 4, Unassigned: 2, Overdue: 1}
	store := &fakeStore{counts: want}
	svc := NewService(store)
	orgID := uuid.New()

	got, err := svc.Get(context.Background(), orgID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != want {
		t.Fatalf("counts: %+v, want %+v", got, want)
	}
	if store.orgID != orgID {
		t.Fatalf("org: %s, want %s", store.orgID, orgID)
	}
}

func TestServicePropagatesErrors(t *testing.T) {
	want := errors.New("database down")
	svc := NewService(&fakeStore{err: want})

	if _, err := svc.Get(context.Background(), uuid.New()); !errors.Is(err, want) {
		t.Fatalf("expected store error, got %v", err)
	}
}
