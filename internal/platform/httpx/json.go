package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxBodyBytes = 1 << 20

// DecodeJSON decodes a single JSON object. Decode failures are returned as
// safe *APIError values: raw decoder text is never exposed to the client.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return decodeJSONError(err)
	}

	if dec.More() {
		return BadRequest("invalid_json", "request body must contain a single JSON object")
	}

	return nil
}

func decodeJSONError(err error) error {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return &APIError{
			Status:  http.StatusRequestEntityTooLarge,
			Code:    "payload_too_large",
			Message: fmt.Sprintf("request body must not exceed %d bytes", maxBodyBytes),
		}
	}

	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		if typeErr.Field != "" {
			return BadRequest("invalid_json", fmt.Sprintf("field %q has the wrong type", typeErr.Field))
		}
		return BadRequest("invalid_json", "request body has a value of the wrong type")
	}

	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return BadRequest("invalid_json", "request body contains malformed JSON")
	}

	if errors.Is(err, io.EOF) {
		return BadRequest("invalid_json", "request body is empty")
	}

	if strings.Contains(err.Error(), "unknown field") {
		return BadRequest("unknown_field", "request body contains an unknown field")
	}

	return BadRequest("invalid_json", "request body is invalid")
}

func WriteJSON(w http.ResponseWriter, status int, data any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(data)
}
