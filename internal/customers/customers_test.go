package customers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type fakeStore struct {
	orgID     uuid.UUID
	id        uuid.UUID
	pseudonym string
	actor     requestctx.Principal
	err       error
}

func (f *fakeStore) Erase(_ context.Context, orgID, id uuid.UUID, pseudonym string, actor requestctx.Principal) (Customer, error) {
	f.orgID, f.id, f.pseudonym, f.actor = orgID, id, pseudonym, actor
	if f.err != nil {
		return Customer{}, f.err
	}
	return Customer{ID: id, OrgID: orgID, Email: pseudonym}, nil
}

func TestAnonymizedEmailIsStableAndOrgScoped(t *testing.T) {
	orgID := uuid.New()
	customerID := uuid.New()

	got := AnonymizedEmail(orgID, customerID)
	if got != AnonymizedEmail(orgID, customerID) {
		t.Fatal("pseudonym is not deterministic")
	}
	if !strings.HasPrefix(got, "erased-") || !strings.HasSuffix(got, "@"+PseudonymDomain) {
		t.Fatalf("unexpected pseudonym %q", got)
	}
	if got == AnonymizedEmail(uuid.New(), customerID) {
		t.Fatal("pseudonym is not scoped to the org")
	}
	if got == AnonymizedEmail(orgID, uuid.New()) {
		t.Fatal("pseudonym is not scoped to the customer")
	}
	if len(got) > 255 {
		t.Fatalf("pseudonym too long: %d", len(got))
	}
}

func TestServiceErasesWithScopedPseudonym(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store)
	orgID, customerID := uuid.New(), uuid.New()
	actor := requestctx.Principal{Kind: requestctx.KindUser, ActorID: uuid.New(), OrgID: orgID, Role: "admin"}

	if _, err := svc.Erase(context.Background(), orgID, customerID, actor); err != nil {
		t.Fatalf("erase: %v", err)
	}
	if store.orgID != orgID || store.id != customerID {
		t.Fatalf("store scoped to %s/%s", store.orgID, store.id)
	}
	if store.pseudonym != AnonymizedEmail(orgID, customerID) {
		t.Fatalf("pseudonym=%q", store.pseudonym)
	}
	if store.actor != actor {
		t.Fatalf("actor=%+v", store.actor)
	}
}

func handlerFor(svc customerService) *chi.Mux {
	h := NewHandler(svc)
	r := chi.NewRouter()
	r.Delete("/v1/customers/{id}/data", h.Erase)
	return r
}

func withPrincipal(r *http.Request, p requestctx.Principal) *http.Request {
	return r.WithContext(requestctx.WithPrincipal(r.Context(), p))
}

func TestHandlerEraseNoContent(t *testing.T) {
	store := &fakeStore{}
	orgID := uuid.New()
	customerID := uuid.New()
	req := withPrincipal(httptest.NewRequest(http.MethodDelete, "/v1/customers/"+customerID.String()+"/data", nil),
		requestctx.Principal{Kind: requestctx.KindUser, ActorID: uuid.New(), OrgID: orgID, Role: "owner"})
	rec := httptest.NewRecorder()
	handlerFor(NewService(store)).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.id != customerID || store.orgID != orgID {
		t.Fatalf("store args=%s/%s", store.orgID, store.id)
	}
}

func TestHandlerEraseInvalidID(t *testing.T) {
	req := withPrincipal(httptest.NewRequest(http.MethodDelete, "/v1/customers/nope/data", nil),
		requestctx.Principal{Kind: requestctx.KindUser, ActorID: uuid.New(), OrgID: uuid.New(), Role: "owner"})
	rec := httptest.NewRecorder()
	handlerFor(NewService(&fakeStore{})).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestHandlerEraseNotFound(t *testing.T) {
	store := &fakeStore{err: ErrCustomerNotFound}
	customerID := uuid.New()
	req := withPrincipal(httptest.NewRequest(http.MethodDelete, "/v1/customers/"+customerID.String()+"/data", nil),
		requestctx.Principal{Kind: requestctx.KindUser, ActorID: uuid.New(), OrgID: uuid.New(), Role: "owner"})
	rec := httptest.NewRecorder()
	handlerFor(NewService(store)).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !errors.Is(store.err, ErrCustomerNotFound) {
		t.Fatal("unexpected error")
	}
}
