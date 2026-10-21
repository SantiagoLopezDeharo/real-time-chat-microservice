package ws

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Options struct {
	MaxQueuedBytes int
	WriteWait      time.Duration
	PongWait       time.Duration
	PingPeriod     time.Duration
	ReadLimit      int64
}

func DefaultOptions() Options {
	return Options{
		MaxQueuedBytes: 4 << 20,
		WriteWait:      10 * time.Second,
		PongWait:       60 * time.Second,
		PingPeriod:     54 * time.Second,
		ReadLimit:      512,
	}
}

var (
	closeNormal   = websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	closeSlow     = websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "slow consumer")
	closeShutdown = websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down")
)

type outbound struct {
	payload []byte
	ping    bool
}

// Client buffers outbound messages in a queue bounded by payload bytes, so a
// burst never evicts a healthy reader while a stalled one cannot pin unbounded
// memory. A writer goroutine exists only while the queue has work and pings are
// driven by a timer, so an idle client costs a single parked reader goroutine.
type Client struct {
	conn     *websocket.Conn
	userID   string
	opts     Options
	onClosed func()

	mu          sync.Mutex
	queue       []outbound
	spare       []outbound
	queuedBytes int
	writing     bool
	closeFrame  []byte
	pinger      *time.Timer
}

func newClient(conn *websocket.Conn, userID string, opts Options, onClosed func()) *Client {
	return &Client{conn: conn, userID: userID, opts: opts, onClosed: onClosed}
}

// Serve registers conn for the user and starts its reader. It returns
// immediately so the HTTP handler can exit and release the request state.
func (h *Hub) Serve(conn *websocket.Conn, userID string) {
	c := newClient(conn, userID, h.opts, h.clients.Done)
	if !h.register(c) {
		_ = conn.WriteControl(websocket.CloseMessage, closeShutdown, time.Now().Add(h.opts.WriteWait))
		_ = conn.Close()
		return
	}

	c.mu.Lock()
	c.pinger = time.AfterFunc(h.opts.PingPeriod, c.ping)
	c.mu.Unlock()

	go func() {
		c.readPump()
		h.unregister(c)
		c.close(closeNormal)
	}()
}

// enqueue reports false when the client is closing or the payload would exceed
// the queue budget. A payload larger than the budget is still accepted into an
// empty queue.
func (c *Client) enqueue(payload []byte) bool {
	c.mu.Lock()
	if c.closeFrame != nil || (len(c.queue) > 0 && c.queuedBytes+len(payload) > c.opts.MaxQueuedBytes) {
		c.mu.Unlock()
		return false
	}
	c.queue = append(c.queue, outbound{payload: payload})
	c.queuedBytes += len(payload)
	start := c.claimWriter()
	c.mu.Unlock()

	if start {
		go c.flush()
	}
	return true
}

func (c *Client) ping() {
	c.mu.Lock()
	if c.closeFrame != nil {
		c.mu.Unlock()
		return
	}
	c.queue = append(c.queue, outbound{ping: true})
	c.pinger.Reset(c.opts.PingPeriod)
	start := c.claimWriter()
	c.mu.Unlock()

	if start {
		go c.flush()
	}
}

// close asks the writer to send frame and release the connection. Only the
// first call has an effect.
func (c *Client) close(frame []byte) {
	c.mu.Lock()
	if c.closeFrame != nil {
		c.mu.Unlock()
		return
	}
	c.closeFrame = frame
	start := c.claimWriter()
	c.mu.Unlock()

	if start {
		go c.flush()
	}
}

// claimWriter must be called with c.mu held and reports whether the caller
// must start the writer goroutine.
func (c *Client) claimWriter() bool {
	if c.writing {
		return false
	}
	c.writing = true
	return true
}

func (c *Client) flush() {
	for {
		c.mu.Lock()
		if c.closeFrame != nil {
			frame := c.closeFrame
			c.mu.Unlock()
			c.finish(frame)
			return
		}
		batch := c.takeQueueLocked()
		if len(batch) == 0 {
			c.writing = false
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()

		if err := c.write(batch); err != nil {
			c.close(closeNormal)
			continue
		}
		c.release(batch)
	}
}

func (c *Client) write(batch []outbound) error {
	for _, out := range batch {
		deadline := time.Now().Add(c.opts.WriteWait)
		if out.ping {
			if err := c.conn.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
				return err
			}
			continue
		}
		_ = c.conn.SetWriteDeadline(deadline)
		if err := c.conn.WriteMessage(websocket.TextMessage, out.payload); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) finish(frame []byte) {
	c.mu.Lock()
	if c.pinger != nil {
		c.pinger.Stop()
	}
	c.queue, c.spare = nil, nil
	c.mu.Unlock()

	_ = c.conn.WriteControl(websocket.CloseMessage, frame, time.Now().Add(c.opts.WriteWait))
	_ = c.conn.Close()
	c.onClosed()
}

// takeQueueLocked hands the queued batch to the writer and swaps in the spare
// buffer. The spare is cleared so the two never share a backing array.
func (c *Client) takeQueueLocked() []outbound {
	batch := c.queue
	c.queue, c.spare = c.spare[:0], nil
	return batch
}

func (c *Client) release(batch []outbound) {
	written := 0
	for _, out := range batch {
		written += len(out.payload)
	}
	clear(batch)
	c.mu.Lock()
	c.queuedBytes -= written
	c.spare = batch[:0]
	c.mu.Unlock()
}

// readPump discards inbound data; it exists to process control frames and to
// detect dead peers through the pong deadline.
func (c *Client) readPump() {
	c.conn.SetReadLimit(c.opts.ReadLimit)
	_ = c.conn.SetReadDeadline(time.Now().Add(c.opts.PongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(c.opts.PongWait))
	})

	for {
		if _, _, err := c.conn.NextReader(); err != nil {
			return
		}
	}
}
