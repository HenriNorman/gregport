package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/HenriNorman/gregport/internal/platform/logging"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// FieldError describes one invalid field in a failed request.
type FieldError struct {
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// APIError is both the domain error type and the HTTP representation of a
// failure. Domain sentinels are *APIError values, so errors.Is keeps working
// and a code/status is declared next to the error it belongs to.
type APIError struct {
	Status  int          `json:"-"`
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Details []FieldError `json:"details"`
	Err     error        `json:"-"`
	logged  bool
}

// MarkLogged tells WriteError that this failure has already been recorded, so
// it should not log it a second time.
func (e *APIError) MarkLogged() *APIError {
	if e != nil {
		e.logged = true
	}
	return e
}

func (e *APIError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *APIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string       `json:"code"`
	Message   string       `json:"message"`
	Details   []FieldError `json:"details"`
	RequestID string       `json:"request_id"`
}

// WriteError is the single write path for every failure. It uses the
// *APIError carried by the error chain (domain sentinels are *APIError), and
// turns anything else into a generic 500 while logging the real cause.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := "internal"
	message := "internal error"
	details := []FieldError{}

	var ae *APIError
	if errors.As(err, &ae) && ae != nil {
		status = ae.Status
		code = ae.Code
		message = ae.Message
		if ae.Details != nil {
			details = ae.Details
		}
	}

	if status >= http.StatusInternalServerError {
		if ae == nil || !ae.logged {
			logging.FromContext(r.Context()).Error("request failed",
				"error", err,
				"method", r.Method,
				"route", RoutePattern(r.Context()),
			)
		}
		status, code, message, details = http.StatusInternalServerError, "internal", "internal error", []FieldError{}
	}

	_ = WriteJSON(w, status, errorEnvelope{Error: errorBody{
		Code:      code,
		Message:   message,
		Details:   details,
		RequestID: requestctx.RequestID(r.Context()),
	}})
}

// RoutePattern returns the matched chi route pattern, or "unmatched". The raw
// path is deliberately never returned because it contains ids.
func RoutePattern(ctx context.Context) string {
	if rctx := chi.RouteContext(ctx); rctx != nil {
		if p := rctx.RoutePattern(); p != "" {
			return p
		}
	}
	return "unmatched"
}

func BadRequest(code, msg string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: code, Message: msg}
}

func Unauthorized(code, msg string) *APIError {
	return &APIError{Status: http.StatusUnauthorized, Code: code, Message: msg}
}

func Forbidden(code, msg string) *APIError {
	return &APIError{Status: http.StatusForbidden, Code: code, Message: msg}
}

func NotFound(code, msg string) *APIError {
	return &APIError{Status: http.StatusNotFound, Code: code, Message: msg}
}

func Conflict(code, msg string) *APIError {
	return &APIError{Status: http.StatusConflict, Code: code, Message: msg}
}

func Unprocessable(code, msg string) *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: code, Message: msg}
}

func PreconditionFailed(code, msg string) *APIError {
	return &APIError{Status: http.StatusPreconditionFailed, Code: code, Message: msg}
}

func PreconditionRequired(code, msg string) *APIError {
	return &APIError{Status: http.StatusPreconditionRequired, Code: code, Message: msg}
}

func InternalError(err error) *APIError {
	return &APIError{
		Status:  http.StatusInternalServerError,
		Code:    "internal",
		Message: "internal error",
		Err:     err,
	}
}
