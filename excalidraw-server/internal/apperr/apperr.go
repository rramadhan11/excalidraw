// Package apperr defines the errors stores return for client-caused failures
// and maps them to HTTP responses, so every handler answers the same way.
package apperr

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/httpx"
)

var (
	// ErrNotFound is also used for resources the user may not access, so
	// their existence is never revealed.
	ErrNotFound = errors.New("not found")
	// ErrForbidden is for members whose role doesn't allow the action.
	ErrForbidden = errors.New("not allowed for your role")
)

// Error is a client error with a specific status and machine-readable code.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func Invalid(format string, args ...any) error {
	return &Error{Status: http.StatusBadRequest, Code: "invalid", Message: fmt.Sprintf(format, args...)}
}

func NotFound(message string) error {
	return &Error{Status: http.StatusNotFound, Code: "not_found", Message: message}
}

func Conflict(code, message string) error {
	return &Error{Status: http.StatusConflict, Code: code, Message: message}
}

func TooLarge(message string) error {
	return &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: message}
}

// Is reports whether err is a client error with the given status.
func Is(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

// Write sends the response for err. Errors that aren't client errors are
// logged and hidden behind a generic 500.
func Write(w http.ResponseWriter, logger *slog.Logger, err error) {
	var e *Error
	switch {
	case errors.As(err, &e):
		httpx.WriteError(w, e.Status, e.Code, e.Message)
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "forbidden", err.Error())
	default:
		logger.Error("request failed", slog.Any("err", err))
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}
