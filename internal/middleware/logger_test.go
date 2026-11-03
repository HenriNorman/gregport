package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/logging"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func testRouter(buf *bytes.Buffer, routes func(chi.Router)) http.Handler {
	logger := logging.NewWithWriter(buf, slog.LevelDebug)

	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(WithLogger(logger))
	r.Use(RequestLogger())
	r.Use(Recovery)
	if routes != nil {
		routes(r)
	}
	return r
}

func decodeLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not JSON: %v (%q)", err, line)
		}
		out = append(out, entry)
	}
	return out
}

func TestRequestLogger_FieldsAndRoutePattern(t *testing.T) {
	var buf bytes.Buffer
	userID := uuid.New()
	orgID := uuid.New()

	router := testRouter(&buf, func(r chi.Router) {
		r.Get("/things/{id}", func(w http.ResponseWriter, r *http.Request) {
			requestctx.WithPrincipal(r.Context(), requestctx.Principal{
				Kind:    requestctx.KindUser,
				ActorID: userID,
				OrgID:   orgID,
				Role:    "agent",
			})
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("hello"))
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/things/abc123?token=supersecret", nil)
	req.Header.Set("X-Request-ID", "req-123")
	req.Header.Set("Authorization", "Bearer secret-token-value")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got != "req-123" {
		t.Fatalf("request id header: got %q", got)
	}

	logs := decodeLogLines(t, &buf)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log line, got %d: %s", len(logs), buf.String())
	}
	entry := logs[0]

	want := map[string]any{
		"method":     http.MethodGet,
		"route":      "/things/{id}",
		"status":     float64(http.StatusCreated),
		"bytes":      float64(len("hello")),
		"request_id": "req-123",
		"org_id":     orgID.String(),
		"user_id":    userID.String(),
		"level":      "INFO",
		"msg":        "request",
	}
	for k, v := range want {
		if entry[k] != v {
			t.Errorf("%s: got %v, want %v", k, entry[k], v)
		}
	}
	if _, ok := entry["duration_ms"]; !ok {
		t.Error("missing duration_ms")
	}

	raw := buf.String()
	for _, secret := range []string{"abc123", "supersecret", "secret-token-value", "hello"} {
		if strings.Contains(raw, secret) {
			t.Errorf("log leaked %q: %s", secret, raw)
		}
	}
}

func TestRequestLogger_GeneratesRequestID(t *testing.T) {
	var buf bytes.Buffer
	router := testRouter(&buf, func(r chi.Router) {
		r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		})
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ok", nil))

	headerID := rec.Header().Get("X-Request-ID")
	if headerID == "" {
		t.Fatal("expected generated request id header")
	}
	logs := decodeLogLines(t, &buf)
	if got := logs[0]["request_id"]; got != headerID {
		t.Fatalf("log request_id %v != header %q", got, headerID)
	}
}

func TestRequestLogger_NoPrincipal(t *testing.T) {
	var buf bytes.Buffer
	router := testRouter(&buf, func(r chi.Router) {
		r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ok", nil))

	entry := decodeLogLines(t, &buf)[0]
	if _, ok := entry["org_id"]; ok {
		t.Fatalf("unexpected org_id: %+v", entry)
	}
	if _, ok := entry["user_id"]; ok {
		t.Fatalf("unexpected user_id: %+v", entry)
	}
	if entry["status"] != float64(http.StatusNoContent) {
		t.Fatalf("status: %v", entry["status"])
	}
}

func TestRequestLogger_APIKeyPrincipal(t *testing.T) {
	var buf bytes.Buffer
	keyID := uuid.New()
	orgID := uuid.New()

	router := testRouter(&buf, func(r chi.Router) {
		r.Get("/ok", func(w http.ResponseWriter, r *http.Request) {
			requestctx.WithPrincipal(r.Context(), requestctx.Principal{
				Kind:    requestctx.KindAPIKey,
				ActorID: keyID,
				OrgID:   orgID,
				Role:    "agent",
			})
			w.WriteHeader(http.StatusOK)
		})
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ok", nil))

	entry := decodeLogLines(t, &buf)[0]
	if entry["api_key_id"] != keyID.String() {
		t.Fatalf("api_key_id: %v", entry["api_key_id"])
	}
	if _, ok := entry["user_id"]; ok {
		t.Fatalf("api key must not be logged as user_id: %+v", entry)
	}
}

func TestRequestLogger_ServerErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	router := testRouter(&buf, func(r chi.Router) {
		r.Get("/boom", func(w http.ResponseWriter, r *http.Request) {
			httpx.WriteError(w, r, httpx.InternalError(errors.New("database exploded")))
		})
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d", rec.Code)
	}

	logs := decodeLogLines(t, &buf)
	var access map[string]any
	var loggedCause bool
	for _, entry := range logs {
		if entry["msg"] == "request" {
			access = entry
		}
		if entry["msg"] == "request failed" && strings.Contains(entry["error"].(string), "database exploded") {
			loggedCause = true
		}
	}
	if access == nil {
		t.Fatalf("missing access log: %s", buf.String())
	}
	if access["level"] != "ERROR" || access["status"] != float64(http.StatusInternalServerError) {
		t.Fatalf("unexpected access entry: %+v", access)
	}
	if !loggedCause {
		t.Fatalf("real error was not logged: %s", buf.String())
	}
	if strings.Contains(rec.Body.String(), "database exploded") {
		t.Fatalf("response leaked internal error text: %s", rec.Body.String())
	}
}

func TestRequestLogger_UnmatchedRoute(t *testing.T) {
	var buf bytes.Buffer
	router := testRouter(&buf, func(r chi.Router) {
		r.Get("/only", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/does/not/exist", nil))

	entry := decodeLogLines(t, &buf)[0]
	if entry["route"] != "unmatched" {
		t.Fatalf("route: %v", entry["route"])
	}
	if strings.Contains(buf.String(), "/does/not/exist") {
		t.Fatalf("log leaked raw path: %s", buf.String())
	}
}

func TestWithLogger_ServiceLoggerCarriesRequestID(t *testing.T) {
	var buf bytes.Buffer
	router := testRouter(&buf, func(r chi.Router) {
		r.Get("/svc", func(_ http.ResponseWriter, r *http.Request) {
			logging.FromContext(r.Context()).Info("service log", "work", "done")
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/svc", nil)
	req.Header.Set("X-Request-ID", "svc-req")
	router.ServeHTTP(httptest.NewRecorder(), req)

	logs := decodeLogLines(t, &buf)
	var found bool
	for _, entry := range logs {
		if entry["msg"] == "service log" {
			found = true
			if entry["request_id"] != "svc-req" {
				t.Fatalf("service log request_id: %v", entry["request_id"])
			}
			if entry["work"] != "done" {
				t.Fatalf("service log attrs: %+v", entry)
			}
		}
	}
	if !found {
		t.Fatalf("service log not emitted: %s", buf.String())
	}
}

func TestResponseRecorder_IgnoresDoubleWriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	rr := &responseRecorder{ResponseWriter: rec, status: http.StatusOK}

	rr.WriteHeader(http.StatusTeapot)
	rr.WriteHeader(http.StatusInternalServerError)

	if rr.status != http.StatusTeapot {
		t.Fatalf("status: got %d", rr.status)
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("underlying status: got %d", rec.Code)
	}
}
