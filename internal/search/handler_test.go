package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type fakeService struct {
	got    Filter
	result Result
	err    error
}

func (f *fakeService) Search(_ context.Context, _ requestctx.Principal, filter Filter) (Result, error) {
	f.got = filter
	return f.result, f.err
}

func searchRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	p := requestctx.Principal{
		Kind:    requestctx.KindUser,
		ActorID: uuid.New(),
		OrgID:   uuid.New(),
		Role:    "agent",
	}
	return req.WithContext(requestctx.WithPrincipal(req.Context(), p))
}

func TestHandlerParsesFiltersAndWritesResults(t *testing.T) {
	id := uuid.New()
	assignee := uuid.New()
	svc := &fakeService{result: Result{
		Hits: []TicketHit{{
			ID:               id,
			Number:           42,
			Subject:          "Printer failure",
			Status:           "open",
			Priority:         "high",
			AssigneeID:       &assignee,
			RequesterEmail:   "h@acme.com",
			CreatedAt:        time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
			UpdatedAt:        time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
			SubjectHighlight: "Printer <mark>failure</mark>",
		}},
		NextCursor: "next",
	}}
	h := NewHandler(svc)

	req := searchRequest(t, "/v1/search?q=printer&status=open&tag=hardware&assignee="+assignee.String()+"&cursor=abc&limit=10")
	rr := httptest.NewRecorder()
	h.Search(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if svc.got.Query != "printer" || svc.got.Cursor != "abc" || svc.got.Limit != 10 {
		t.Fatalf("filter = %+v", svc.got)
	}
	if svc.got.Status == nil || *svc.got.Status != "open" {
		t.Fatalf("status filter = %v", svc.got.Status)
	}
	if svc.got.Tag == nil || *svc.got.Tag != "hardware" {
		t.Fatalf("tag filter = %v", svc.got.Tag)
	}
	if svc.got.AssigneeID == nil || *svc.got.AssigneeID != assignee {
		t.Fatalf("assignee filter = %v", svc.got.AssigneeID)
	}

	var body SearchResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Results) != 1 || body.Results[0].ID != id.String() {
		t.Fatalf("results = %+v", body.Results)
	}
	if body.Results[0].Highlights.Subject == "" {
		t.Fatalf("missing subject highlight: %+v", body.Results[0])
	}
	if body.NextCursor != "next" {
		t.Fatalf("next cursor = %q", body.NextCursor)
	}
}

func TestHandlerRejectsInvalidStatus(t *testing.T) {
	h := NewHandler(&fakeService{})
	rr := httptest.NewRecorder()
	h.Search(rr, searchRequest(t, "/v1/search?status=archived"))

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "invalid_search_filter" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestHandlerRejectsInvalidAssignee(t *testing.T) {
	h := NewHandler(&fakeService{})
	rr := httptest.NewRecorder()
	h.Search(rr, searchRequest(t, "/v1/search?assignee=not-a-uuid"))

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}
}

func TestHandlerDefaultsLimit(t *testing.T) {
	svc := &fakeService{}
	h := NewHandler(svc)
	rr := httptest.NewRecorder()
	h.Search(rr, searchRequest(t, "/v1/search"))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if svc.got.Limit != DefaultLimit {
		t.Fatalf("limit = %d, want %d", svc.got.Limit, DefaultLimit)
	}
}
