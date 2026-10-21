package ws

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func fastOptions() Options {
	opts := DefaultOptions()
	opts.WriteWait = time.Second
	return opts
}

func newTestServer(t *testing.T, h *Hub) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		h.Serve(conn, r.URL.Query().Get("user"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dial(t *testing.T, srv *httptest.Server, userID string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/?" + url.Values{"user": {userID}}.Encode()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func connect(t *testing.T, h *Hub, srv *httptest.Server, userID string, n int) []*websocket.Conn {
	t.Helper()
	before := h.ConnectionCounts([]string{userID})[userID]
	conns := make([]*websocket.Conn, n)
	for i := range conns {
		conns[i] = dial(t, srv, userID)
	}
	waitFor(t, "connections registered", func() bool { return h.ConnectionCounts([]string{userID})[userID] == before+n })
	return conns
}

func connections(h *Hub, userID string) int {
	return h.ConnectionCounts([]string{userID})[userID]
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func readText(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if kind != websocket.TextMessage {
		t.Fatalf("message type = %d, want text", kind)
	}
	return string(data)
}

func expectClose(t *testing.T, conn *websocket.Conn, code int) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		if !websocket.IsCloseError(err, code) {
			t.Fatalf("read error = %v, want close code %d", err, code)
		}
		return
	}
}

// fakeClient has no connection; its writer is pre-claimed so tests drive the
// queue by hand and no flush goroutine ever starts.
func fakeClient(userID string, maxBytes int) *Client {
	c := newClient(nil, userID, Options{MaxQueuedBytes: maxBytes}, func() {})
	c.writing = true
	return c
}

func queued(c *Client) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

func takeQueue(c *Client) []outbound {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.takeQueueLocked()
}

func drain(c *Client) {
	c.release(takeQueue(c))
}

func isClosed(c *Client) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeFrame != nil
}
