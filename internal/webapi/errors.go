package webapi

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"

	"github.com/home-operations/kritika/internal/store"
)

// ErrorCode is the machine-readable part of an API error.
type ErrorCode string

// Error codes the API returns.
const (
	CodeNotFound      ErrorCode = "not_found"
	CodeBadRequest    ErrorCode = "bad_request"
	CodeInvalidCursor ErrorCode = "invalid_cursor"
	CodeAmbiguous     ErrorCode = "ambiguous"
	CodeInternal      ErrorCode = "internal"

	CodeForbidden     ErrorCode = "forbidden"
	CodeNoHead        ErrorCode = "no_head"
	CodeNotCancelable ErrorCode = "not_cancelable"
	CodeAlreadyQueued ErrorCode = "already_queued"
	// CodeForgeError is the forge failing or refusing a call an admin
	// asked for; the message is the forge's.
	CodeForgeError ErrorCode = "forge_error"
	// CodeInstallationServed refuses to uninstall an App from an account
	// its connection serves.
	CodeInstallationServed ErrorCode = "installation_served"
	// CodeNotAnApp refuses an installation route for a connection that is
	// no GitHub App.
	CodeNotAnApp ErrorCode = "not_an_app"
)

// apiError is an error a handler returns to be written as ErrorBody.
type apiError struct {
	status  int
	code    ErrorCode
	message string
}

func (e *apiError) Error() string { return string(e.code) + ": " + e.message }

func errNotFound(what string) error {
	return &apiError{status: http.StatusNotFound, code: CodeNotFound, message: what + " not found"}
}

func errBadRequest(code ErrorCode, message string) error {
	return &apiError{status: http.StatusBadRequest, code: code, message: message}
}

// errStatus is an error with any status.
func errStatus(status int, code ErrorCode, message string) error {
	return &apiError{status: status, code: code, message: message}
}

// jsonOptions is how every API response is encoded: json/v2's semantics,
// under which a nil slice or map is an empty one, as the UI's types
// promise, with map members sorted so a response reads the same twice.
var jsonOptions = jsonv2.JoinOptions(jsonv2.Deterministic(true))

// writeJSON writes v as the response. Every API response is no-store:
// it is per-principal and must never be served from a shared cache.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = jsonv2.MarshalWrite(w, v, jsonOptions) // a failed write has no one to report to
}

// classify is the status and ErrorBody err is answered with: an apiError
// as itself, a store miss as 404, a bad cursor or filter as 400. Anything
// else is unexpected, false, and a 500 that says nothing about its cause.
func classify(err error) (int, ErrorBody, bool) {
	if e, ok := errors.AsType[*apiError](err); ok {
		return e.status, ErrorBody{Code: e.code, Message: e.message}, true
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, ErrorBody{Code: CodeNotFound, Message: "not found"}, true
	case errors.Is(err, store.ErrCursor):
		return http.StatusBadRequest, ErrorBody{Code: CodeInvalidCursor, Message: "cursor is not valid"}, true
	case errors.Is(err, store.ErrFilter), errors.Is(err, store.ErrPageLimit):
		return http.StatusBadRequest, ErrorBody{Code: CodeBadRequest, Message: "invalid filter"}, true
	}
	return http.StatusInternalServerError, ErrorBody{Code: CodeInternal, Message: "internal error"}, false
}

// writeError writes err as classify answers it, logging an unexpected one.
func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	status, body, expected := classify(err)
	if !expected {
		if r.Context().Err() != nil {
			return // the client went away; there is no one to answer
		}
		logger.ErrorContext(r.Context(), "webapi: request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	}
	writeJSON(w, status, body)
}
