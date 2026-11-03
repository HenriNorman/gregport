package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeInto(t *testing.T, body string, dst any) *APIError {
	t.Helper()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()

	err := DecodeJSON(rec, r, dst)
	if err == nil {
		return nil
	}

	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	return ae
}

func TestDecodeJSON_Valid(t *testing.T) {
	var dst struct {
		Name string `json:"name"`
	}

	if err := decodeInto(t, `{"name":"ok"}`, &dst); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}
	if dst.Name != "ok" {
		t.Fatalf("name: got %q", dst.Name)
	}
}

func TestDecodeJSON_Malformed(t *testing.T) {
	var dst struct{}
	err := decodeInto(t, `{"name":`, &dst)

	if err == nil || err.Code != "invalid_json" || err.Status != http.StatusBadRequest {
		t.Fatalf("unexpected error: %+v", err)
	}
	if strings.Contains(err.Message, "unexpected end of JSON input") {
		t.Fatalf("leaked raw decoder text: %q", err.Message)
	}
}

func TestDecodeJSON_UnknownField(t *testing.T) {
	var dst struct {
		Name string `json:"name"`
	}
	err := decodeInto(t, `{"nope":true}`, &dst)

	if err == nil || err.Code != "unknown_field" || err.Status != http.StatusBadRequest {
		t.Fatalf("unexpected error: %+v", err)
	}
	if strings.Contains(err.Message, "nope") {
		t.Fatalf("leaked field name in unknown-field message: %q", err.Message)
	}
}

func TestDecodeJSON_WrongType(t *testing.T) {
	var dst struct {
		Count int `json:"count"`
	}
	err := decodeInto(t, `{"count":"many"}`, &dst)

	if err == nil || err.Code != "invalid_json" || err.Status != http.StatusBadRequest {
		t.Fatalf("unexpected error: %+v", err)
	}
	if !strings.Contains(err.Message, `"count"`) {
		t.Fatalf("expected field name in message, got %q", err.Message)
	}
}

func TestDecodeJSON_MultipleObjects(t *testing.T) {
	var dst struct {
		Name string `json:"name"`
	}
	err := decodeInto(t, `{"name":"a"}{"name":"b"}`, &dst)

	if err == nil || err.Code != "invalid_json" || err.Status != http.StatusBadRequest {
		t.Fatalf("unexpected error: %+v", err)
	}
}

func TestDecodeJSON_EmptyBody(t *testing.T) {
	var dst struct{}
	err := decodeInto(t, ``, &dst)

	if err == nil || err.Code != "invalid_json" || err.Status != http.StatusBadRequest {
		t.Fatalf("unexpected error: %+v", err)
	}
	if err.Message != "request body is empty" {
		t.Fatalf("message: %q", err.Message)
	}
}

func TestDecodeJSON_TooLarge(t *testing.T) {
	var dst struct {
		Name string `json:"name"`
	}
	body := `{"name":"` + strings.Repeat("x", maxBodyBytes) + `"}`

	err := decodeInto(t, body, &dst)
	if err == nil || err.Code != "payload_too_large" || err.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("unexpected error: %+v", err)
	}
}
