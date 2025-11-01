package ws

import (
	"log"
	"sync"
)

type Hub struct {
	Register   chan *Client
	Unregister chan *Client
	Broadcast  chan *BroadcastMessage
	channels   map[string]*Channel
	mu         sync.Mutex
}

type BroadcastMessage struct {
	ChannelID string
	Message   []byte
}

func NewHub() *Hub {
	return &Hub{
		Register:   make(chan *Client),
		Unregister: make(chan *Client),
		Broadcast:  make(chan *BroadcastMessage),
		channels:   make(map[string]*Channel),
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
	channel, ok := h.channels[client.channelID]
	if !ok {
		channel = NewChannel()
		h.channels[client.channelID] = channel
	}
	channel.AddClient(client)
	log.Printf("client registered to channel %s, total in channel=%d", client.channelID, len(channel.clients))
}

func (h *Hub) unregisterClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if channel, ok := h.channels[client.channelID]; ok {
		channel.RemoveClient(client)
		log.Printf("client unregistered from channel %s, total in channel=%d", client.channelID, len(channel.clients))
	}
}

func (h *Hub) broadcastMessage(broadcastMessage *BroadcastMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if channel, ok := h.channels[broadcastMessage.ChannelID]; ok {
		channel.Broadcast(broadcastMessage.Message)
	}
}

func (h *Hub) GetClientCounts(channelIDs []string) map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()

	counts := make(map[string]int)
	for _, channelID := range channelIDs {
		if channel, ok := h.channels[channelID]; ok {
			channel.mu.Lock()
			counts[channelID] = len(channel.clients)
			channel.mu.Unlock()
		} else {
			counts[channelID] = 0
		}
	}
	return counts
}
