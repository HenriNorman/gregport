package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HenriNorman/gregport/internal/platform/logging"
)

type fakePinger struct {
	err         error
	hasDeadline bool
}

func (f *fakePinger) Ping(ctx context.Context) error {
	_, f.hasDeadline = ctx.Deadline()
	return f.err
}

func TestLive_DoesNotDependOnDatabase(t *testing.T) {
	rec := httptest.NewRecorder()
	Live(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type: %q", got)
	}
	assertStatus(t, rec.Body.Bytes(), "ok")
}

func TestReady_Healthy(t *testing.T) {
	db := &fakePinger{}
	rec := httptest.NewRecorder()
	Ready(db)(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}
	if !db.hasDeadline {
		t.Fatal("readiness ping has no deadline")
	}
	assertStatus(t, rec.Body.Bytes(), "ok")
}

func TestReady_UnhealthyIsUnreadyButHidesCause(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.NewWithWriter(&buf, slog.LevelDebug).With("request_id", "req-ready")

	db := &fakePinger{err: errors.New("connect: password=hunter2")}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	r = r.WithContext(logging.WithLogger(r.Context(), logger))

	Ready(db)(rec, r)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d", rec.Code)
	}
	assertStatus(t, rec.Body.Bytes(), "unready")
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("response leaked the database error: %s", rec.Body.String())
	}
	if !strings.Contains(buf.String(), "readiness check failed") {
		t.Fatalf("readiness failure was not logged: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Fatalf("expected WARN level: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"request_id":"req-ready"`) {
		t.Fatalf("log missing request_id: %s", buf.String())
	}
}

func assertStatus(t *testing.T, body []byte, want string) {
	t.Helper()

	var got struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("invalid JSON: %v (%q)", err, body)
	}
	if got.Status != want {
		t.Fatalf("status field: got %q, want %q", got.Status, want)
	}
}
