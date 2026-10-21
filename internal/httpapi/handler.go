package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"chat-microservice/pkg/models"

	"github.com/gorilla/websocket"
)

type Chat interface {
	Send(sender string, participants []string, content string) (*models.Message, error)
	History(ctx context.Context, userID string, participants []string, page, size int) ([]*models.Message, error)
}

type Connections interface {
	Serve(conn *websocket.Conn, userID string)
	ConnectionCounts(users []string) map[string]int
}

type Handler struct {
	chat         Chat
	conns        Connections
	maxBodyBytes int64
	upgrader     websocket.Upgrader
}

// NewHandler authenticates websockets through the Authorization header, which
// browsers cannot attach cross-origin, so every origin is accepted. Write
// buffers are pooled and read buffers kept small because clients only receive.
func NewHandler(chat Chat, conns Connections, maxBodyBytes int64) *Handler {
	return &Handler{
		chat:         chat,
		conns:        conns,
		maxBodyBytes: maxBodyBytes,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  512,
			WriteBufferPool: &sync.Pool{},
			CheckOrigin:     func(*http.Request) bool { return true },
		},
	}
}

func (h *Handler) Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "time": time.Now().Format(time.RFC3339)})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
