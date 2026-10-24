package httpx

import (
	"encoding/json"
	"net/http"
)

const maxBodyBytes = 1 << 20 

func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return err
	}

	if dec.More() {
		return &APIError{
			Status:  http.StatusBadRequest,
			Code:    "invalid_json",
			Message: "request body must contain a single JSON object",
		}
	}

	return nil
}

func WriteJSON(w http.ResponseWriter, status int, data any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(data)
}
