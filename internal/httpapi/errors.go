package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"chat-microservice/internal/service"
	"chat-microservice/pkg/models"
)

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, models.ErrNoParticipants),
		errors.Is(err, models.ErrEmptyParticipant),
		errors.Is(err, models.ErrTooManyParticipants),
		errors.Is(err, service.ErrEmptyContent):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, service.ErrNotParticipant):
		http.Error(w, "forbidden: user must be part of participants", http.StatusForbidden)
	case errors.Is(err, service.ErrOverloaded), errors.Is(err, service.ErrClosed):
		w.Header().Set("Retry-After", "1")
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	default:
		slog.Error("request failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
