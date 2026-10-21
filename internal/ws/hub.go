package ws

import (
	"context"
	"sync"
)

// Hub tracks every websocket connection by user ID. Broadcasts run on the
// caller's goroutine under a read lock, so concurrent senders fan out in
// parallel and never wait on slow clients.
type Hub struct {
	opts Options

	mu     sync.RWMutex
	users  map[string]map[*Client]struct{}
	closed bool

	clients sync.WaitGroup
}

func NewHub(opts Options) *Hub {
	return &Hub{opts: opts, users: make(map[string]map[*Client]struct{})}
}

func (h *Hub) register(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	conns, ok := h.users[c.userID]
	if !ok {
		conns = make(map[*Client]struct{})
		h.users[c.userID] = conns
	}
	conns[c] = struct{}{}
	h.clients.Add(1)
	return true
}

func (h *Hub) unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	conns, ok := h.users[c.userID]
	if !ok {
		return
	}
	delete(conns, c)
	if len(conns) == 0 {
		delete(h.users, c.userID)
	}
}

// Broadcast queues payload on every connection of every recipient and returns
// how many connections accepted it. Connections whose queue is over budget are
// disconnected. Recipients share the payload, so it must not be modified after
// the call.
func (h *Hub) Broadcast(recipients []string, payload []byte) int {
	h.mu.RLock()
	delivered := 0
	var slow []*Client
	for _, userID := range recipients {
		for c := range h.users[userID] {
			if c.enqueue(payload) {
				delivered++
			} else {
				slow = append(slow, c)
			}
		}
	}
	h.mu.RUnlock()

	for _, c := range slow {
		c.close(closeSlow)
	}
	return delivered
}

func (h *Hub) ConnectionCounts(users []string) map[string]int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	counts := make(map[string]int, len(users))
	for _, userID := range users {
		counts[userID] = len(h.users[userID])
	}
	return counts
}

// Shutdown rejects new connections, asks every connected client to close and
// waits for their close frames to be written or ctx to expire.
func (h *Hub) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	h.closed = true
	var all []*Client
	for _, conns := range h.users {
		for c := range conns {
			all = append(all, c)
		}
	}
	h.mu.Unlock()

	for _, c := range all {
		c.close(closeShutdown)
	}

	done := make(chan struct{})
	go func() {
		h.clients.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
