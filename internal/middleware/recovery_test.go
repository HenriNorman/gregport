package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type testEnvelope struct {
	Error struct {
		Code      string           `json:"code"`
		Message   string           `json:"message"`
		Details   []map[string]any `json:"details"`
		RequestID string           `json:"request_id"`
	} `json:"error"`
}

func decodeErrorEnvelope(t *testing.T, body []byte) testEnvelope {
	t.Helper()

	var env testEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("invalid error envelope: %v (%q)", err, body)
	}
	return env
}

func TestRecovery_PanicReturnsEnvelopeAndKeepsServing(t *testing.T) {
	var buf bytes.Buffer
	router := testRouter(&buf, func(r chi.Router) {
		r.Get("/panic", func(http.ResponseWriter, *http.Request) {
			panic("kaboom")
		})
		r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("fine"))
		})
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", rec.Code)
	}
	env := decodeErrorEnvelope(t, rec.Body.Bytes())
	if env.Error.Code != "internal" {
		t.Fatalf("error code: got %q", env.Error.Code)
	}
	if env.Error.Message != "internal error" {
		t.Fatalf("message: got %q", env.Error.Message)
	}
	if env.Error.Details == nil || len(env.Error.Details) != 0 {
		t.Fatalf("details must be an empty array: %#v", env.Error.Details)
	}
	if env.Error.RequestID == "" || env.Error.RequestID != rec.Header().Get("X-Request-ID") {
		t.Fatalf("request_id %q != header %q", env.Error.RequestID, rec.Header().Get("X-Request-ID"))
	}

	logs := decodeLogLines(t, &buf)
	if len(logs) != 2 {
		t.Fatalf("expected panic + access log, got %d: %s", len(logs), buf.String())
	}
	panicEntry := logs[0]
	if panicEntry["level"] != "ERROR" || panicEntry["msg"] != "panic recovered" {
		t.Fatalf("unexpected panic entry: %+v", panicEntry)
	}
	if panicEntry["panic"] != "kaboom" {
		t.Fatalf("panic value: %v", panicEntry["panic"])
	}
	if stack, _ := panicEntry["stack"].(string); !strings.Contains(stack, "goroutine") {
		t.Fatalf("missing stack: %v", panicEntry["stack"])
	}
	if panicEntry["request_id"] == "" {
		t.Fatal("panic log missing request_id")
	}
	if logs[1]["status"] != float64(http.StatusInternalServerError) {
		t.Fatalf("access log status: %v", logs[1]["status"])
	}

	// The server keeps serving after the panic.
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if rec2.Code != http.StatusOK || rec2.Body.String() != "fine" {
		t.Fatalf("server did not recover: %d %q", rec2.Code, rec2.Body.String())
	}
}

func TestRecovery_RepanicsAbortHandler(t *testing.T) {
	router := chi.NewRouter()
	router.Use(Recovery)
	router.Get("/abort", func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})

	defer func() {
		if rvr := recover(); rvr != http.ErrAbortHandler {
			t.Fatalf("expected ErrAbortHandler, got %v", rvr)
		}
	}()

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort", nil))
	t.Fatal("expected panic to propagate")
}
