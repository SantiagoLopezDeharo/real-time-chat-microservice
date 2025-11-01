package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
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
	channelID := r.URL.Query().Get("channel")
	if channelID == "" {
		http.Error(w, "channel ID is required", http.StatusBadRequest)
		return
	}

	claims := middleware.GetUserClaims(r)
	if !middleware.CanAccessChannel(claims, channelID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade: %v", err)
		return
	}

	client := ws.NewClient(conn, h.svc.Hub(), channelID)
	client.Start()
}

func (h *Handler) HandleSendMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	channelID := r.URL.Path[len("/api/messages/"):]
	if channelID == "" {
		http.Error(w, "channel ID is required", http.StatusBadRequest)
		return
	}

	claims := middleware.GetUserClaims(r)
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var payload struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	msg := &models.Message{
		Sender:    claims.ID,
		Content:   payload.Content,
		CreatedAt: time.Now(),
		ChannelID: channelID,
	}

	if err := h.svc.BroadcastMessage(msg); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) HandleGetClientCounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		Channels []string `json:"channels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if len(payload.Channels) == 0 {
		http.Error(w, "channels array is required", http.StatusBadRequest)
		return
	}

	counts := h.svc.Hub().GetClientCounts(payload.Channels)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(counts)
}

func (h *Handler) HandleGetMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	channelID := r.URL.Path[len("/api/messages/"):]
	if channelID == "" || channelID == "counts" {
		http.Error(w, "channel ID is required", http.StatusBadRequest)
		return
	}

	claims := middleware.GetUserClaims(r)
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	messages, err := h.svc.GetMessagesForChannel(channelID, claims.ID, claims.Groups)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(messages)
}

func (h *Handler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.HandleGetMessages(w, r)
	} else if r.Method == http.MethodPost {
		h.HandleSendMessage(w, r)
	} else {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
