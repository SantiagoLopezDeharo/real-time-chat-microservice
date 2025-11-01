package ws

import (
	"sync"
)

type Channel struct {
	clients   map[*Client]bool
	broadcast chan []byte
	mu        sync.Mutex
}

func NewChannel() *Channel {
	return &Channel{
		clients:   make(map[*Client]bool),
		broadcast: make(chan []byte),
	}
}

func (c *Channel) AddClient(client *Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clients[client] = true
}

func (c *Channel) RemoveClient(client *Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.clients[client]; ok {
		delete(c.clients, client)
		close(client.send)
	}
}

func (c *Channel) Broadcast(message []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for client := range c.clients {
		go func(client *Client) {
			select {
			case client.send <- message:
			default:
				c.RemoveClient(client)
			}
		}(client)
	}
}
