package middleware

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestTimeout_RealServerReturnsEnvelope(t *testing.T) {
	var buf bytes.Buffer
	logger := testLogger(&buf)

	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(WithLogger(logger))
	r.Use(RequestLogger())
	r.Use(Recovery)
	r.Use(Timeout(50 * time.Millisecond))
	r.Get("/slow", func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(srv.URL + "/slow")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type: %q", got)
	}
	env := decodeErrorEnvelope(t, body)
	if env.Error.Code != "request_timeout" {
		t.Fatalf("code: got %q", env.Error.Code)
	}
	requestID := resp.Header.Get("X-Request-ID")
	if requestID == "" || env.Error.RequestID != requestID {
		t.Fatalf("request_id: body %q header %q", env.Error.RequestID, requestID)
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
	if access["route"] != "/slow" {
		t.Fatalf("access route: %v", access["route"])
	}
	if access["status"] != float64(http.StatusServiceUnavailable) {
		t.Fatalf("access status: %v", access["status"])
	}
}

func TestTimeout_RealServerSuccessPath(t *testing.T) {
	var buf bytes.Buffer
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(WithLogger(testLogger(&buf)))
	r.Use(RequestLogger())
	r.Use(Recovery)
	r.Use(Timeout(time.Second))
	r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ok")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"ok":true}` {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
}

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
