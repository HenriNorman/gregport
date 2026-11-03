package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HenriNorman/gregport/internal/platform/logging"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type testEnvelope struct {
	Error struct {
		Code      string       `json:"code"`
		Message   string       `json:"message"`
		Details   []FieldError `json:"details"`
		RequestID string       `json:"request_id"`
	} `json:"error"`
}

func requestWithID(id string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/tickets/123", nil)
	return r.WithContext(requestctx.WithRequestID(r.Context(), id))
}

func writeAndDecode(t *testing.T, r *http.Request, err error) (*httptest.ResponseRecorder, testEnvelope) {
	t.Helper()

	rec := httptest.NewRecorder()
	WriteError(rec, r, err)

	var env testEnvelope
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &env); decodeErr != nil {
		t.Fatalf("invalid envelope: %v (%q)", decodeErr, rec.Body.String())
	}
	return rec, env
}

func TestWriteError_DomainError(t *testing.T) {
	err := NotFound("ticket_not_found", "ticket not found")
	rec, env := writeAndDecode(t, requestWithID("req-1"), err)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d", rec.Code)
	}
	if env.Error.Code != "ticket_not_found" || env.Error.Message != "ticket not found" {
		t.Fatalf("unexpected body: %+v", env.Error)
	}
	if env.Error.RequestID != "req-1" {
		t.Fatalf("request_id: got %q", env.Error.RequestID)
	}
	if env.Error.Details == nil || len(env.Error.Details) != 0 {
		t.Fatalf("details must be an empty array, got %#v", env.Error.Details)
	}
	if !strings.Contains(rec.Body.String(), `"details":[]`) {
		t.Fatalf("details must render as []: %s", rec.Body.String())
	}
}

func TestWriteError_WrappedDomainError(t *testing.T) {
	err := fmt.Errorf("service: %w", Conflict("conflict", "already exists"))
	rec, env := writeAndDecode(t, requestWithID("req-2"), err)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d", rec.Code)
	}
	if env.Error.Code != "conflict" {
		t.Fatalf("code: got %q", env.Error.Code)
	}
}

func TestWriteError_UnknownErrorIsGenericButLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.NewWithWriter(&buf, slog.LevelDebug).With("request_id", "req-3")

	r := requestWithID("req-3")
	r = r.WithContext(logging.WithLogger(r.Context(), logger))

	rec, env := writeAndDecode(t, r, errors.New("connect: password=hunter2"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d", rec.Code)
	}
	if env.Error.Code != "internal" || env.Error.Message != "internal error" {
		t.Fatalf("unexpected body: %+v", env.Error)
	}
	if env.Error.RequestID != "req-3" {
		t.Fatalf("request_id: got %q", env.Error.RequestID)
	}
	if env.Error.Details == nil {
		t.Fatal("details must be an empty array")
	}

	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("response leaked internal error: %s", rec.Body.String())
	}
	logs := buf.String()
	if !strings.Contains(logs, "request failed") || !strings.Contains(logs, "hunter2") {
		t.Fatalf("internal error was not logged: %s", logs)
	}
	if !strings.Contains(logs, `"request_id":"req-3"`) {
		t.Fatalf("log missing request_id: %s", logs)
	}
}

func TestWriteError_PreservesDetails(t *testing.T) {
	err := Unprocessable("validation_failed", "one or more fields are invalid")
	err.Details = []FieldError{
		{Field: "email", Message: "must be a valid email"},
		{Field: "password", Message: "too short"},
	}

	_, env := writeAndDecode(t, requestWithID("req-4"), err)

	if len(env.Error.Details) != 2 {
		t.Fatalf("details: %#v", env.Error.Details)
	}
	if env.Error.Details[0].Field != "email" {
		t.Fatalf("details[0]: %#v", env.Error.Details[0])
	}
}

func TestWriteError_DoesNotMutateSharedError(t *testing.T) {
	shared := InternalError(errors.New("boom"))

	first, _ := writeAndDecode(t, requestWithID("first"), shared)
	second, _ := writeAndDecode(t, requestWithID("second"), shared)

	if first.Code != http.StatusInternalServerError || second.Code != http.StatusInternalServerError {
		t.Fatalf("statuses: %d %d", first.Code, second.Code)
	}
	if shared.Code != "internal" || shared.Message != "internal error" {
		t.Fatalf("shared error was mutated: %+v", shared)
	}
}

func TestWriteError_DeadlineExceeded(t *testing.T) {
	rec, env := writeAndDecode(t, requestWithID("req-timeout"),
		fmt.Errorf("list tickets: %w", context.DeadlineExceeded))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d", rec.Code)
	}
	if env.Error.Code != "request_timeout" || env.Error.Message != "request timed out" {
		t.Fatalf("unexpected body: %+v", env.Error)
	}
	if env.Error.RequestID != "req-timeout" {
		t.Fatalf("request_id: got %q", env.Error.RequestID)
	}
	if env.Error.Details == nil || len(env.Error.Details) != 0 {
		t.Fatalf("details must be an empty array: %#v", env.Error.Details)
	}
}

func TestWriteError_PreservesServiceUnavailable(t *testing.T) {
	rec, env := writeAndDecode(t, requestWithID("req-503"),
		ServiceUnavailable("maintenance", "try again later"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d", rec.Code)
	}
	if env.Error.Code != "maintenance" {
		t.Fatalf("code: got %q", env.Error.Code)
	}
}

func TestWriteError_NilError(t *testing.T) {
	rec, env := writeAndDecode(t, requestWithID("req-5"), nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d", rec.Code)
	}
	if env.Error.Code != "internal" {
		t.Fatalf("code: got %q", env.Error.Code)
	}
}

func TestWriteError_MissingRequestID(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	_, env := writeAndDecode(t, r, BadRequest("bad", "bad request"))

	if env.Error.RequestID != "" {
		t.Fatalf("request_id: got %q", env.Error.RequestID)
	}
}
