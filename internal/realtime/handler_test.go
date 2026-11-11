package realtime

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type fakeTokens struct {
	mu      sync.Mutex
	tokens  map[string]requestctx.Principal
	counter int
}

func newFakeTokens() *fakeTokens {
	return &fakeTokens{tokens: make(map[string]requestctx.Principal)}
}

func (f *fakeTokens) Issue(_ context.Context, p requestctx.Principal) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counter++
	token := "stream-token-" + strconv.Itoa(f.counter)
	f.tokens[token] = p
	return token, time.Now().Add(30 * time.Second), nil
}

func (f *fakeTokens) Consume(_ context.Context, token string) (requestctx.Principal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.tokens[token]
	if !ok {
		return requestctx.Principal{}, ErrInvalidToken
	}
	delete(f.tokens, token)
	return p, nil
}

func (f *fakeTokens) preload(p requestctx.Principal) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	token := "preloaded-" + uuid.NewString()
	f.tokens[token] = p
	return token
}

type fakeTyping struct {
	mu      sync.Mutex
	orgID   uuid.UUID
	number  int64
	actorID uuid.UUID
	err     error
}

func (f *fakeTyping) Typing(_ context.Context, orgID uuid.UUID, number int64, actorID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orgID, f.number, f.actorID = orgID, number, actorID
	return f.err
}

func TestIssueStreamTokenReturnsSingleUseCredential(t *testing.T) {
	tokens := newFakeTokens()
	handler := NewHandler(tokens, nil, nil, nil)
	p := requestctx.Principal{OrgID: uuid.New(), ActorID: uuid.New(), Role: "agent"}

	r := httptest.NewRequest(http.MethodPost, "/v1/stream-tokens", nil)
	r = r.WithContext(requestctx.WithPrincipal(r.Context(), p))
	rec := httptest.NewRecorder()
	handler.IssueStreamToken(rec, r)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: %d", rec.Code)
	}
	var body StreamTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Token == "" {
		t.Fatal("empty token")
	}
	got, err := tokens.Consume(context.Background(), body.Token)
	if err != nil || got != p {
		t.Fatalf("stored principal: %+v err=%v", got, err)
	}
}

func TestStreamRejectsInvalidToken(t *testing.T) {
	handler := NewHandler(newFakeTokens(), nil, nil, nil)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/stream?token=nope", nil)

	handler.Stream(rec, r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "invalid_stream_token") {
		t.Fatalf("body: %s", rec.Body)
	}
}

func TestStreamReplaysThenDeliversLive(t *testing.T) {
	orgID := uuid.New()
	tokens := newFakeTokens()
	token := tokens.preload(requestctx.Principal{OrgID: orgID, ActorID: uuid.New()})

	backplane := newFakeBackplane()
	replayer := &fakeReplayer{events: []Event{
		{Seq: 1, Type: "ticket.created"},
		{Seq: 2, Type: "ticket.status_changed"},
		{Seq: 3, Type: "comment.added"},
	}}
	hub := NewHub(backplane, replayer, nil, HubOptions{Heartbeat: time.Hour, BufferSize: 8})
	defer hub.Close()

	handler := NewHandler(tokens, hub, nil, nil)
	server := httptest.NewServer(http.HandlerFunc(handler.Stream))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"?token="+token, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type: %q", got)
	}

	ids := make(chan int64, 8)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "id: ") {
				seq, err := strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
				if err == nil {
					ids <- seq
				}
			}
		}
	}()

	expectID := func(want int64) {
		t.Helper()
		select {
		case got := <-ids:
			if got != want {
				t.Fatalf("event id: got %d, want %d", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for event %d", want)
		}
	}

	// Only events after Last-Event-ID are replayed.
	expectID(2)
	expectID(3)

	if err := backplane.PublishEvent(context.Background(), orgID, Event{Seq: 4, Type: "ticket.assigned"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	expectID(4)

	// The token is consumed by the first request.
	resp.Body.Close()
	second, err := http.Get(server.URL + "?token=" + token)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused token status: %d", second.StatusCode)
	}
}

func TestStreamRejectsInvalidLastEventID(t *testing.T) {
	tokens := newFakeTokens()
	token := tokens.preload(requestctx.Principal{OrgID: uuid.New()})
	handler := NewHandler(tokens, nil, nil, nil)

	r := httptest.NewRequest(http.MethodGet, "/v1/stream?token="+token, nil)
	r.Header.Set("Last-Event-ID", "not-a-number")
	rec := httptest.NewRecorder()
	handler.Stream(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestTypingPublishesOrgScopedIndicator(t *testing.T) {
	typing := &fakeTyping{}
	handler := NewHandler(nil, nil, typing, nil)

	orgID, actorID := uuid.New(), uuid.New()
	p := requestctx.Principal{OrgID: orgID, ActorID: actorID, Role: "agent"}

	router := chi.NewRouter()
	router.Post("/v1/tickets/{number}/typing", handler.Typing)
	req := httptest.NewRequest(http.MethodPost, "/v1/tickets/42/typing", nil)
	req = req.WithContext(requestctx.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body)
	}
	if typing.orgID != orgID || typing.number != 42 || typing.actorID != actorID {
		t.Fatalf("typing call: org=%s number=%d actor=%s", typing.orgID, typing.number, typing.actorID)
	}
}

func TestTypingOtherOrgTicketIsNotFound(t *testing.T) {
	typing := &fakeTyping{err: ErrTicketNotFound}
	handler := NewHandler(nil, nil, typing, nil)

	router := chi.NewRouter()
	router.Post("/v1/tickets/{number}/typing", handler.Typing)
	req := httptest.NewRequest(http.MethodPost, "/v1/tickets/7/typing", nil)
	req = req.WithContext(requestctx.WithPrincipal(req.Context(), requestctx.Principal{OrgID: uuid.New()}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestTypingRejectsInvalidNumber(t *testing.T) {
	handler := NewHandler(nil, nil, &fakeTyping{}, nil)

	router := chi.NewRouter()
	router.Post("/v1/tickets/{number}/typing", handler.Typing)
	req := httptest.NewRequest(http.MethodPost, "/v1/tickets/nope/typing", nil)
	req = req.WithContext(requestctx.WithPrincipal(req.Context(), requestctx.Principal{OrgID: uuid.New()}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rec.Code)
	}
}
