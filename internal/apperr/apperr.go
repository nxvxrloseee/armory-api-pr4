// Package apperr defines the domain error type shared by every handler and
// the single place that turns it into the HTTP error envelope from
// КОНТРАКТ-API.md: {"message": "...", "errors": {...}} with the matching
// status code.
package apperr

import (
	"encoding/json"
	"net/http"
)

type Kind int

const (
	KindValidation Kind = iota
	KindConflict
	KindNotFound
	KindBadRequest
)

// AppError is the only error type handlers are expected to construct
// deliberately; anything else reaching Write is treated as a 500.
type AppError struct {
	Kind    Kind
	Message string
	Errors  map[string]string // 422 only: field name -> message
	Count   int               // 409 only: how many active records still reference this one
}

func (e *AppError) Error() string { return e.Message }

func Validation(message string, errs map[string]string) *AppError {
	return &AppError{Kind: KindValidation, Message: message, Errors: errs}
}

func Conflict(message string, count int) *AppError {
	return &AppError{Kind: KindConflict, Message: message, Count: count}
}

func NotFound(message string) *AppError {
	return &AppError{Kind: KindNotFound, Message: message}
}

func BadRequest(message string) *AppError {
	return &AppError{Kind: KindBadRequest, Message: message}
}

func statusFor(k Kind) int {
	switch k {
	case KindValidation:
		return http.StatusUnprocessableEntity
	case KindConflict:
		return http.StatusConflict
	case KindNotFound:
		return http.StatusNotFound
	case KindBadRequest:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// Write serializes err as the contract's error body. A plain (non-*AppError)
// error never leaks its internal text to the client — it becomes a generic
// 500, and the caller is expected to have logged it already.
func Write(w http.ResponseWriter, err error) {
	appErr, ok := err.(*AppError)
	if !ok {
		WriteJSON(w, http.StatusInternalServerError, map[string]any{"message": "Внутренняя ошибка сервера"})
		return
	}
	body := map[string]any{"message": appErr.Message}
	if appErr.Kind == KindValidation && appErr.Errors != nil {
		body["errors"] = appErr.Errors
	}
	if appErr.Kind == KindConflict && appErr.Count > 0 {
		body["count"] = appErr.Count
	}
	WriteJSON(w, statusFor(appErr.Kind), body)
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
