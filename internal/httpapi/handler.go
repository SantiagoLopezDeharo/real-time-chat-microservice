package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chat-microservice/internal/middleware"
	"chat-microservice/internal/service"
	"chat-microservice/internal/ws"
	"chat-microservice/pkg/models"

	"github.com/gorilla/websocket"
)

type Handler struct {
	svc      *service.ChatService
	upgrader websocket.Upgrader
}

func NewHandler(svc *service.ChatService) *Handler {
	return &Handler{
		svc: svc,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
	}
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "time": time.Now().Format(time.RFC3339)})
}

func (h *Handler) HandleWebsocket(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetUserClaims(r)
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade: %v", err)
		return
	}

	client := ws.NewClient(conn, h.svc.Hub(), claims.ID)
	client.Start()
}

func (h *Handler) HandleSendMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	claims := middleware.GetUserClaims(r)
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var payload struct {
		Participants []string `json:"participants"`
		Content      string   `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if len(payload.Participants) == 0 {
		http.Error(w, "participants array is required", http.StatusBadRequest)
		return
	}

	if !models.ContainsUser(payload.Participants, claims.ID) {
		http.Error(w, "forbidden: sender must be part of participants", http.StatusForbidden)
		return
	}

	msg := &models.Message{
		Sender:       claims.ID,
		Content:      payload.Content,
		CreatedAt:    time.Now(),
		Participants: payload.Participants,
	}

	if err := h.svc.BroadcastMessage(msg); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "message queued"})
}

func (h *Handler) HandleGetMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	claims := middleware.GetUserClaims(r)
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	participantsStr := r.URL.Query().Get("participants")
	if participantsStr == "" {
		http.Error(w, "participants query parameter is required", http.StatusBadRequest)
		return
	}

	participants := strings.Split(participantsStr, ",")
	for i, p := range participants {
		participants[i] = strings.TrimSpace(p)
	}

	page := 0
	size := 50

	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p >= 0 {
			page = p
		}
	}

	if sizeStr := r.URL.Query().Get("size"); sizeStr != "" {
		if s, err := strconv.Atoi(sizeStr); err == nil && s > 0 {
			size = s

			if size > 100 {
				size = 100
			}
		}
	}

	messages, err := h.svc.GetMessagesForChannelWithPagination(participants, claims.ID, page, size)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(messages)
}

func (h *Handler) HandleGetUserConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		Users []string `json:"users"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if len(payload.Users) == 0 {
		http.Error(w, "users array is required", http.StatusBadRequest)
		return
	}

	counts := h.svc.Hub().GetChannelParticipantCounts(payload.Users)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(counts)
}
