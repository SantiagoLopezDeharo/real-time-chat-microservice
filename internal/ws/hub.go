package ws

import (
	"log"
	"sync"
)

type Hub struct {
	Register         chan *Client
	Unregister       chan *Client
	Broadcast        chan *BroadcastMessage
	clients          map[string]map[*Client]bool
	mu               sync.RWMutex
	broadcastQueue   chan *broadcastJob
	numBcastWorkers  int
	numBcastJobQueue int
}

type broadcastJob struct {
	client  *Client
	message []byte
}

type BroadcastMessage struct {
	Participants []string
	Message      []byte
	SenderID     string
}

func NewHub() *Hub {
	return &Hub{
		Register:         make(chan *Client),
		Unregister:       make(chan *Client),
		Broadcast:        make(chan *BroadcastMessage),
		clients:          make(map[string]map[*Client]bool),
		broadcastQueue:   make(chan *broadcastJob, 1024),
		numBcastWorkers:  4,
		numBcastJobQueue: 1024,
	}
}

func (h *Hub) Run() {
	for i := 0; i < h.numBcastWorkers; i++ {
		go h.broadcastWorker()
	}

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

func (h *Hub) broadcastWorker() {
	for job := range h.broadcastQueue {
		select {
		case job.client.send <- job.message:
		default:

			h.Unregister <- job.client
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
			if len(userClients) == 0 {
				delete(h.clients, client.userID)
			}
		}
	}
}

func (h *Hub) broadcastMessage(broadcastMessage *BroadcastMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, participantID := range broadcastMessage.Participants {
		if participantID == broadcastMessage.SenderID {
			continue
		}

		if userClients, ok := h.clients[participantID]; ok {
			for client := range userClients {
				h.broadcastQueue <- &broadcastJob{
					client:  client,
					message: broadcastMessage.Message,
				}
			}
		}
	}
}

func (h *Hub) GetUserConnectionCount(userID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if userClients, ok := h.clients[userID]; ok {
		return len(userClients)
	}
	return 0
}

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
