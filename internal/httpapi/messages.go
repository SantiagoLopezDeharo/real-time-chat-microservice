package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"chat-microservice/internal/middleware"
)

const (
	defaultPageSize = 50
	maxPageSize     = 100
	maxPage         = 10_000
)

func (h *Handler) HandleSendMessage(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.GetUserID(r)

	var payload struct {
		Participants []string `json:"participants"`
		Content      string   `json:"content"`
	}
	if !decodeJSON(w, r, h.maxBodyBytes, &payload) {
		return
	}

	msg, err := h.chat.Send(userID, payload.Participants, payload.Content)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "message queued", "id": msg.ID.Hex()})
}

func (h *Handler) HandleGetMessages(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.GetUserID(r)
	query := r.URL.Query()

	participantsStr := query.Get("participants")
	if participantsStr == "" {
		http.Error(w, "participants query parameter is required", http.StatusBadRequest)
		return
	}

	page := intParam(query.Get("page"), 0, 0)
	if page > maxPage {
		http.Error(w, "page out of range", http.StatusBadRequest)
		return
	}
	size := min(intParam(query.Get("size"), defaultPageSize, 1), maxPageSize)

	messages, err := h.chat.History(r.Context(), userID, strings.Split(participantsStr, ","), page, size)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, messages)
}

// intParam parses a query parameter, falling back when it is missing, not a
// number or below minimum.
func intParam(raw string, fallback, minimum int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum {
		return fallback
	}
	return value
}
