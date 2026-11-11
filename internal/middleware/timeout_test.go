package middleware

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

func TestTimeoutExcept_SkipsOnlyConfiguredPath(t *testing.T) {
	var buf bytes.Buffer
	router := testRouter(&buf, func(r chi.Router) {
		r.Use(TimeoutExcept(30*time.Millisecond, "/v1/stream"))
		r.Get("/v1/stream", func(w http.ResponseWriter, r *http.Request) {
			if _, ok := r.Context().Deadline(); ok {
				t.Error("stream context must not have a request deadline")
			}
			time.Sleep(60 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		})
		r.Get("/v1/other", func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		})
	})

	stream := httptest.NewRecorder()
	router.ServeHTTP(stream, httptest.NewRequest(http.MethodGet, "/v1/stream", nil))
	if stream.Code != http.StatusOK {
		t.Fatalf("stream status: %d", stream.Code)
	}

	other := httptest.NewRecorder()
	router.ServeHTTP(other, httptest.NewRequest(http.MethodGet, "/v1/other", nil))
	if other.Code != http.StatusServiceUnavailable {
		t.Fatalf("other status: %d", other.Code)
	}
}

func TestTimeout_FastRequestPassesThrough(t *testing.T) {
	var buf bytes.Buffer
	router := testRouter(&buf, func(r chi.Router) {
		r.Use(Timeout(time.Second))
		r.Get("/fast", func(w http.ResponseWriter, r *http.Request) {
			if _, ok := r.Context().Deadline(); !ok {
				t.Error("handler context has no deadline")
			}
			w.Header().Set("X-Test", "yes")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fast", nil))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d", rec.Code)
	}
	if rec.Header().Get("X-Test") != "yes" {
		t.Fatalf("header lost: %q", rec.Header().Get("X-Test"))
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Fatalf("body: %q", rec.Body.String())
	}
}

func TestTimeout_OverrunReturnsEnvelope(t *testing.T) {
	const requestID = "req-timeout"

	var buf bytes.Buffer
	router := testRouter(&buf, func(r chi.Router) {
		r.Use(Timeout(50 * time.Millisecond))
		r.Get("/slow", func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/slow", nil)
	req.Header.Set("X-Request-ID", requestID)
	rec := httptest.NewRecorder()

	start := time.Now()
	router.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d", rec.Code)
	}
	env := decodeErrorEnvelope(t, rec.Body.Bytes())
	if env.Error.Code != "request_timeout" {
		t.Fatalf("code: got %q", env.Error.Code)
	}
	if env.Error.Message != "request timed out" {
		t.Fatalf("message: got %q", env.Error.Message)
	}
	if env.Error.RequestID != requestID {
		t.Fatalf("request_id: got %q", env.Error.RequestID)
	}
	if env.Error.Details == nil || len(env.Error.Details) != 0 {
		t.Fatalf("details must be an empty array: %#v", env.Error.Details)
	}
	if elapsed > time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}

	var access map[string]any
	for _, entry := range decodeLogLines(t, &buf) {
		if entry["msg"] == "request" {
			access = entry
		}
	}
	if access == nil {
		t.Fatalf("missing access log: %s", buf.String())
	}
	if access["status"] != float64(http.StatusServiceUnavailable) {
		t.Fatalf("access status: %v", access["status"])
	}
	if access["request_id"] != requestID {
		t.Fatalf("access request_id: %v", access["request_id"])
	}
}

func TestTimeout_DeadlineErrorUsesEnvelope(t *testing.T) {
	var buf bytes.Buffer
	router := testRouter(&buf, func(r chi.Router) {
		r.Use(Timeout(50 * time.Millisecond))
		r.Get("/slow", func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
			httpx.WriteError(w, r, fmt.Errorf("list tickets: %w", r.Context().Err()))
		})
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slow", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d", rec.Code)
	}
	env := decodeErrorEnvelope(t, rec.Body.Bytes())
	if env.Error.Code != "request_timeout" {
		t.Fatalf("code: got %q", env.Error.Code)
	}
}

func TestTimeout_PanicKeepsRecoveryEnvelope(t *testing.T) {
	var buf bytes.Buffer
	router := testRouter(&buf, func(r chi.Router) {
		r.Use(Timeout(time.Second))
		r.Get("/panic", func(http.ResponseWriter, *http.Request) {
			panic("kaboom")
		})
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d", rec.Code)
	}
	env := decodeErrorEnvelope(t, rec.Body.Bytes())
	if env.Error.Code != "internal" {
		t.Fatalf("code: got %q", env.Error.Code)
	}
}

func TestTimeout_DisabledWhenNotPositive(t *testing.T) {
	called := false
	h := Timeout(0)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !called || rec.Code != http.StatusNoContent {
		t.Fatalf("handler not called or wrong status: called=%v status=%d", called, rec.Code)
	}
}
