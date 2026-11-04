package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type fakeService struct {
	counts Counts
	err    error
	orgID  uuid.UUID
	calls  int
}

func (f *fakeService) Get(_ context.Context, orgID uuid.UUID) (Counts, error) {
	f.calls++
	f.orgID = orgID
	if f.err != nil {
		return Counts{}, f.err
	}
	return f.counts, nil
}

func newDashboardHandler(svc countsService) http.Handler {
	r := chi.NewRouter()
	r.Get("/dashboard/counts", NewHandler(svc).GetCounts)
	return r
}

func TestGetCountsSuccess(t *testing.T) {
	orgID := uuid.New()
	want := Counts{
		Total:      10,
		Open:       4,
		Unassigned: 3,
		Overdue:    2,
		ByStatus:   StatusCounts{New: 1, Open: 2, Pending: 1, Solved: 4, Closed: 2},
		ByPriority: PriorityCounts{Low: 2, Normal: 4, High: 3, Urgent: 1},
	}
	svc := &fakeService{counts: want}
	rec := httptest.NewRecorder()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/counts", nil)
	req = req.WithContext(requestctx.WithPrincipal(req.Context(), requestctx.Principal{
		Kind: requestctx.KindUser, ActorID: uuid.New(), OrgID: orgID, Role: "agent",
	}))
	newDashboardHandler(svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.orgID != orgID {
		t.Fatalf("org: %s, want %s", svc.orgID, orgID)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type: %q", got)
	}

	var got Counts
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if got != want {
		t.Fatalf("counts: %+v, want %+v", got, want)
	}
}

func TestGetCountsRequiresPrincipal(t *testing.T) {
	svc := &fakeService{}
	rec := httptest.NewRecorder()

	newDashboardHandler(svc).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/dashboard/counts", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", rec.Code)
	}
	if svc.calls != 0 {
		t.Fatalf("service was called %d times", svc.calls)
	}
}

func TestGetCountsInternalError(t *testing.T) {
	svc := &fakeService{err: errors.New("database down")}
	rec := httptest.NewRecorder()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/counts", nil)
	req = req.WithContext(requestctx.WithPrincipal(req.Context(), requestctx.Principal{
		Kind: requestctx.KindUser, ActorID: uuid.New(), OrgID: uuid.New(), Role: "agent",
	}))
	newDashboardHandler(svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: %d", rec.Code)
	}

	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Error.Code != "internal" {
		t.Fatalf("code: %q", envelope.Error.Code)
	}
}
