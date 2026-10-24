package httpx

import (
	"fmt"
	"net/http"
)

type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Err     error  `json:"-"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *APIError) Unwrap() error {
	return e.Err
}

func WriteError(w http.ResponseWriter, err *APIError) {
	if err.Status >= 500 {
		err.Message = "internal error"
		err.Code = "internal"
	}

	envelope := struct {
		Error *APIError `json:"error"`
	}{Error: err}

	_ = WriteJSON(w, err.Status, envelope)
}

func BadRequest(code, msg string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: code, Message: msg}
}

func Unprocessable(code, msg string) *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: code, Message: msg}
}

func Conflict(code, msg string) *APIError {
	return &APIError{Status: http.StatusConflict, Code: code, Message: msg}
}

func InternalError(err error) *APIError {
	return &APIError{Status: http.StatusInternalServerError, Code: "internal", Message: "internal error", Err: err}
}
