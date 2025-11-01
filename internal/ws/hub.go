package ws

import (
	"log"
	"sync"
)

type Hub struct {
	Register   chan *Client
	Unregister chan *Client
	Broadcast  chan *BroadcastMessage
	clients    map[string]map[*Client]bool // userID -> set of clients
	mu         sync.RWMutex
}

type BroadcastMessage struct {
	Participants []string // User IDs that are part of this channel
	Message      []byte
	SenderID     string // To exclude sender from receiving their own message
}

func NewHub() *Hub {
	return &Hub{
		Register:   make(chan *Client),
		Unregister: make(chan *Client),
		Broadcast:  make(chan *BroadcastMessage),
		clients:    make(map[string]map[*Client]bool),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.Register:
			h.registerClient(client)
		case client := <-h.Unregister:
			h.unregisterClient(client)
		case broadcastMessage := <-h.Broadcast:
			h.broadcastMessage(broadcastMessage)
		}
	}
}

func (h *Hub) registerClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.clients[client.userID]; !ok {
		h.clients[client.userID] = make(map[*Client]bool)
	}
	h.clients[client.userID][client] = true
	log.Printf("client registered for user %s, total connections for user=%d", client.userID, len(h.clients[client.userID]))
}

func (h *Hub) unregisterClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if userClients, ok := h.clients[client.userID]; ok {
		if _, exists := userClients[client]; exists {
			delete(userClients, client)
			close(client.send)
			log.Printf("client unregistered from user %s, total connections for user=%d", client.userID, len(userClients))

			// Clean up empty user entries
			if len(userClients) == 0 {
				delete(h.clients, client.userID)
			}
		}
	}
}

func (h *Hub) broadcastMessage(broadcastMessage *BroadcastMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Send message to all participants except the sender
	for _, participantID := range broadcastMessage.Participants {
		// Skip sender
		if participantID == broadcastMessage.SenderID {
			continue
		}

		// Get all WebSocket connections for this participant
		if userClients, ok := h.clients[participantID]; ok {
			for client := range userClients {
				go func(c *Client) {
					select {
					case c.send <- broadcastMessage.Message:
					default:
						// Client's send channel is full, unregister
						h.Unregister <- c
					}
				}(client)
			}
		}
	}
}

// GetUserConnectionCount returns the number of active connections for a specific user
func (h *Hub) GetUserConnectionCount(userID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if userClients, ok := h.clients[userID]; ok {
		return len(userClients)
	}
	return 0
}

// GetChannelParticipantCounts returns which participants in a channel have active connections
func (h *Hub) GetChannelParticipantCounts(participants []string) map[string]int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	counts := make(map[string]int)
	for _, userID := range participants {
		if userClients, ok := h.clients[userID]; ok {
			counts[userID] = len(userClients)
		} else {
			counts[userID] = 0
		}
	}
	return counts
}
