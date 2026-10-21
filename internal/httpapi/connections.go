package httpapi

import (
	"log/slog"
	"net/http"

	"chat-microservice/internal/middleware"
	"chat-microservice/pkg/models"
)

func (h *Handler) HandleWebsocket(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.GetUserID(r)
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Debug("websocket upgrade failed", "error", err)
		return
	}
	h.conns.Serve(conn, userID)
}

func (h *Handler) HandleGetUserConnections(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Users []string `json:"users"`
	}
	if !decodeJSON(w, r, h.maxBodyBytes, &payload) {
		return
	}
	if len(payload.Users) == 0 {
		http.Error(w, "users array is required", http.StatusBadRequest)
		return
	}
	if len(payload.Users) > models.MaxParticipants {
		http.Error(w, "too many users", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, h.conns.ConnectionCounts(payload.Users))
}
