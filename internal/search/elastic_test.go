package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func principalFor(orgID uuid.UUID) requestctx.Principal {
	return requestctx.Principal{Kind: requestctx.KindUser, OrgID: orgID, Role: "agent"}
}

type fakeTicketStore struct {
	rows map[uuid.UUID]TicketHit
	err  error
	got  []uuid.UUID
}

func (f *fakeTicketStore) TicketsByIDs(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]TicketHit, error) {
	f.got = append([]uuid.UUID(nil), ids...)
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[uuid.UUID]TicketHit, len(ids))
	for _, id := range ids {
		if row, ok := f.rows[id]; ok {
			out[id] = row
		}
	}
	return out, nil
}

func newTestElasticBackend(t *testing.T, handler http.Handler, store TicketStore) (*ElasticBackend, *Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := NewClient(ClientConfig{URL: srv.URL, Index: "tickets", Timeout: time.Second})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return NewElasticBackend(client, store), client
}

func row(id uuid.UUID, orgID uuid.UUID, number int64) TicketHit {
	return TicketHit{
		ID:             id,
		OrgID:          orgID,
		Number:         number,
		Subject:        "canonical subject",
		Status:         "open",
		Priority:       "normal",
		RequesterEmail: "h@acme.com",
		CreatedAt:      time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
	}
}

func TestElasticBackendScopesHydratesAndHighlights(t *testing.T) {
	orgID := uuid.New()
	otherOrgID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()
	created := time.UnixMilli(1768464000000).UTC()

	var captured map[string]any
	backend, _ := newTestElasticBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &captured); err != nil {
			t.Errorf("request body: %v", err)
		}
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hits": map[string]any{"hits": []any{
				map[string]any{
					"_id": id1.String(), "_score": 2.5, "sort": []any{2.5, created.UnixMilli(), 7},
					"highlight": map[string]any{
						"subject":       []string{"Printer " + hlStart + "failure" + hlEnd},
						"comments_text": []string{"the " + hlStart + "printer" + hlEnd + " jams", "second fragment"},
					},
				},
				map[string]any{
					"_id": id2.String(), "_score": 1.5, "sort": []any{1.5, created.Add(-time.Minute).UnixMilli(), 3},
				},
			}},
		})
	}), &fakeTicketStore{rows: map[uuid.UUID]TicketHit{
		id1: row(id1, orgID, 7),
		id2: row(id2, orgID, 3),
	}})

	res, err := backend.Search(context.Background(), orgID, Filter{Query: "printer failure", Limit: 25})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 2 || res.Hits[0].ID != id1 || res.Hits[1].ID != id2 {
		t.Fatalf("hits = %+v", res.Hits)
	}
	if res.Hits[0].Rank != 2.5 || res.Hits[0].Number != 7 {
		t.Fatalf("first hit rank/number = %+v", res.Hits[0])
	}
	if !strings.Contains(res.Hits[0].SubjectHighlight, hlStart) {
		t.Fatalf("subject highlight = %q", res.Hits[0].SubjectHighlight)
	}
	if !strings.Contains(res.Hits[0].CommentHighlight, " … ") {
		t.Fatalf("comment highlight did not join fragments: %q", res.Hits[0].CommentHighlight)
	}

	query := captured["query"].(map[string]any)["bool"].(map[string]any)
	filters := query["filter"].([]any)
	orgFilter := filters[0].(map[string]any)["term"].(map[string]any)
	if orgFilter["org_id"] != orgID.String() {
		t.Fatalf("org filter = %v", orgFilter)
	}
	if orgFilter["org_id"] == otherOrgID.String() {
		t.Fatal("search used the wrong org")
	}
	must := query["must"].([]any)[0].(map[string]any)["multi_match"].(map[string]any)
	fields := must["fields"].([]any)
	if len(fields) != 2 || fields[0] != "subject^3" || fields[1] != "comments_text" {
		t.Fatalf("multi_match fields = %v", fields)
	}
	highlight := captured["highlight"].(map[string]any)
	if highlight["pre_tags"].([]any)[0] != hlStart {
		t.Fatalf("pre_tags = %v", highlight["pre_tags"])
	}
	sort := captured["sort"].([]any)
	if len(sort) != 3 {
		t.Fatalf("query sort = %v", sort)
	}
}

func TestElasticBackendSkipsRowsMissingFromPostgres(t *testing.T) {
	orgID := uuid.New()
	kept := uuid.New()
	deleted := uuid.New()

	backend, _ := newTestElasticBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hits": map[string]any{"hits": []any{
				map[string]any{"_id": deleted.String(), "_score": 2.0, "sort": []any{2.0, 1000, 2}},
				map[string]any{"_id": kept.String(), "_score": 1.0, "sort": []any{1.0, 1000, 1}},
			}},
		})
	}), &fakeTicketStore{rows: map[uuid.UUID]TicketHit{kept: row(kept, orgID, 1)}})

	res, err := backend.Search(context.Background(), orgID, Filter{Query: "printer", Limit: 25})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 1 || res.Hits[0].ID != kept {
		t.Fatalf("hits = %+v", res.Hits)
	}
}

func TestElasticBackendPaginationCursor(t *testing.T) {
	orgID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()

	var captured map[string]any
	backend, _ := newTestElasticBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured)
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hits": map[string]any{"hits": []any{
				map[string]any{"_id": id1.String(), "_score": 2.0, "sort": []any{2.0, 1000, 9}},
				map[string]any{"_id": id2.String(), "_score": 1.0, "sort": []any{1.0, 900, 8}},
			}},
		})
	}), &fakeTicketStore{rows: map[uuid.UUID]TicketHit{
		id1: row(id1, orgID, 9),
		id2: row(id2, orgID, 8),
	}})

	res, err := backend.Search(context.Background(), orgID, Filter{Query: "printer", Limit: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 1 || res.Hits[0].ID != id1 {
		t.Fatalf("hits = %+v", res.Hits)
	}
	if res.NextCursor == "" {
		t.Fatal("expected a next cursor")
	}
	cur, err := decodeCursor(res.NextCursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if cur.Rank != 2.0 {
		t.Fatalf("cursor rank = %v", cur.Rank)
	}
	if cur.Number != 9 || cur.ID != id1 || cur.CreatedAt.UnixMilli() != 1000 {
		t.Fatalf("cursor = %+v", cur)
	}
	if _, ok := captured["search_after"]; ok {
		t.Fatal("first page must not send search_after")
	}

	// The second page resumes after the raw hit, not after the hydrated row.
	captured = nil
	_, err = backend.Search(context.Background(), orgID, Filter{Query: "printer", Limit: 1, Cursor: res.NextCursor})
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	after, ok := captured["search_after"].([]any)
	if !ok || len(after) != 3 {
		t.Fatalf("search_after = %v", captured["search_after"])
	}
	if after[0].(float64) != 2.0 || after[1].(float64) != 1000 || after[2].(float64) != 9 {
		t.Fatalf("search_after values = %v", after)
	}
}

func TestElasticBackendEmptyQueryUsesRecencySort(t *testing.T) {
	orgID := uuid.New()
	id := uuid.New()

	var captured map[string]any
	backend, _ := newTestElasticBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured)
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hits": map[string]any{"hits": []any{
				map[string]any{"_id": id.String(), "_score": 0, "sort": []any{1000, 4}},
			}},
		})
	}), &fakeTicketStore{rows: map[uuid.UUID]TicketHit{id: row(id, orgID, 4)}})

	if _, err := backend.Search(context.Background(), orgID, Filter{Limit: 25}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if _, ok := captured["highlight"]; ok {
		t.Fatal("empty query must not request highlights")
	}
	sort := captured["sort"].([]any)
	if len(sort) != 2 {
		t.Fatalf("empty-query sort = %v", sort)
	}
	if _, ok := captured["query"].(map[string]any)["bool"].(map[string]any)["must"]; ok {
		t.Fatal("empty query must not add a must clause")
	}
}

func TestElasticBackendFacetsAndEmptyOrg(t *testing.T) {
	orgID := uuid.New()
	status := "pending"
	tag := "hardware"
	assignee := uuid.New()
	backend, _ := newTestElasticBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("backend must not be called without an org")
	}), &fakeTicketStore{})

	if _, err := backend.Search(context.Background(), uuid.Nil, Filter{}); !errors.Is(err, ErrMissingOrg) {
		t.Fatalf("missing org error = %v, want ErrMissingOrg", err)
	}

	var captured map[string]any
	backend, _ = newTestElasticBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured)
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		_ = json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"hits": []any{}}})
	}), &fakeTicketStore{})

	if _, err := backend.Search(context.Background(), orgID, Filter{Status: &status, Tag: &tag, AssigneeID: &assignee}); err != nil {
		t.Fatalf("search: %v", err)
	}
	filters := captured["query"].(map[string]any)["bool"].(map[string]any)["filter"].([]any)
	if len(filters) != 4 {
		t.Fatalf("filters = %v", filters)
	}
	terms := map[string]string{}
	for _, f := range filters {
		for field, value := range f.(map[string]any)["term"].(map[string]any) {
			terms[field] = value.(string)
		}
	}
	if terms["org_id"] != orgID.String() || terms["status"] != status || terms["tags"] != tag || terms["assignee_id"] != assignee.String() {
		t.Fatalf("terms = %v", terms)
	}
}

func TestElasticBackendErrorsBecome503(t *testing.T) {
	orgID := uuid.New()
	backend, _ := newTestElasticBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `{"error":{"type":"cluster_block_exception","reason":"blocked"}}`)
	}), &fakeTicketStore{})

	svc := NewService(backend)
	p := principalFor(orgID)
	_, err := svc.Search(context.Background(), p, Filter{Query: "printer"})
	var apiErr *httpx.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want an API error", err)
	}
	if apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", apiErr.Status)
	}
	if !strings.Contains(err.Error(), "search is temporarily unavailable") {
		t.Fatalf("error = %v", err)
	}
}

type fallibleBackend struct {
	primaryErr  error
	primaryRes  Result
	fallbackErr error
	fallbackRes Result
	calls       int
}

func (f *fallibleBackend) Search(_ context.Context, _ uuid.UUID, _ Filter) (Result, error) {
	f.calls++
	if f.primaryErr != nil {
		return Result{}, f.primaryErr
	}
	return f.primaryRes, nil
}

type alwaysFallbackBackend struct {
	primaryErr  error
	fallbackRes Result
}

func (f *alwaysFallbackBackend) Search(_ context.Context, _ uuid.UUID, _ Filter) (Result, error) {
	if f.primaryErr != nil {
		return Result{}, f.primaryErr
	}
	return Result{}, nil
}

func TestFallbackBackendPrimarySucceeds(t *testing.T) {
	orgID := uuid.New()
	want := Result{Hits: []TicketHit{{ID: uuid.New(), Subject: "from primary"}}}
	fb := &fallibleBackend{primaryRes: want}
	b := NewFallbackBackend(fb, &fallibleBackend{primaryRes: Result{Hits: []TicketHit{{Subject: "from fallback"}}}})

	res, err := b.Search(context.Background(), orgID, Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Subject != "from primary" {
		t.Fatalf("result = %+v, want primary", res)
	}
	if fb.calls != 1 {
		t.Fatalf("primary calls = %d, want 1", fb.calls)
	}
}

func TestFallbackBackendFallsBackOnUnavailable(t *testing.T) {
	orgID := uuid.New()
	want := Result{Hits: []TicketHit{{ID: uuid.New(), Subject: "from fallback"}}}
	fb := &fallibleBackend{primaryErr: ErrSearchUnavailable}
	b := NewFallbackBackend(fb, &fallibleBackend{primaryRes: want})

	res, err := b.Search(context.Background(), orgID, Filter{Query: "printer"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Subject != "from fallback" {
		t.Fatalf("result = %+v, want fallback", res)
	}
}

func TestFallbackBackendFallsBackOnIndexNotFound(t *testing.T) {
	orgID := uuid.New()
	want := Result{Hits: []TicketHit{{ID: uuid.New(), Subject: "from fallback"}}}
	fb := &fallibleBackend{primaryErr: ErrIndexNotFound}
	b := NewFallbackBackend(fb, &fallibleBackend{primaryRes: want})

	res, err := b.Search(context.Background(), orgID, Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Subject != "from fallback" {
		t.Fatalf("result = %+v, want fallback", res)
	}
}

func TestFallbackBackendNoFallbackOnOtherError(t *testing.T) {
	orgID := uuid.New()
	boom := errors.New("boom")
	fb := &fallibleBackend{primaryErr: boom}
	b := NewFallbackBackend(fb, &fallibleBackend{primaryRes: Result{Hits: []TicketHit{{Subject: "from fallback"}}}})

	if _, err := b.Search(context.Background(), orgID, Filter{}); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
	if fb.calls != 1 {
		t.Fatalf("primary calls = %d, want 1 (no fallback)", fb.calls)
	}
}
