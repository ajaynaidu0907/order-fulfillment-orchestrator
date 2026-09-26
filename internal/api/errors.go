package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
)

// errorBody is the JSON envelope returned for every error response.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// statusForError maps a (possibly wrapped) domain sentinel error to an HTTP
// status code and a stable machine-readable error code.
func statusForError(err error) (int, string) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, domain.ErrValidation):
		return http.StatusBadRequest, "validation_error"
	case errors.Is(err, domain.ErrOrderNotCancellable):
		return http.StatusConflict, "order_not_cancellable"
	case errors.Is(err, domain.ErrInsufficientInventory):
		return http.StatusConflict, "insufficient_inventory"
	case errors.Is(err, domain.ErrConflict):
		return http.StatusConflict, "conflict"
	case errors.Is(err, domain.ErrPaymentDeclined):
		return http.StatusPaymentRequired, "payment_declined"
	case errors.Is(err, domain.ErrRateLimited):
		return http.StatusTooManyRequests, "rate_limited"
	default:
		return http.StatusInternalServerError, "internal_error"
	}
}

// writeError maps err to a status code and writes the JSON error envelope. 5xx
// responses are logged at error level and never leak internal detail to callers.
func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	status, code := statusForError(err)
	message := err.Error()
	if status >= http.StatusInternalServerError {
		if logger != nil {
			logger.ErrorContext(r.Context(), "request failed",
				"error", err.Error(),
				"method", r.Method,
				"path", r.URL.Path,
			)
		}
		message = "internal server error"
	}
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}
