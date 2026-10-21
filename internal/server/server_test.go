package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"chat-microservice/internal/config"
	"chat-microservice/internal/httpapi"
	"chat-microservice/internal/service"
	"chat-microservice/internal/ws"
	"chat-microservice/pkg/models"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

type memStore struct {
	mu   sync.Mutex
	msgs []*models.Message
}

func (m *memStore) SaveBatch(_ context.Context, msgs []*models.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, msgs...)
	return nil
}

func (m *memStore) History(_ context.Context, participants []string, page, size int) ([]*models.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*models.Message{}
	for i := len(m.msgs) - 1; i >= 0; i-- {
		if slices.Equal(m.msgs[i].Participants, participants) {
			out = append(out, m.msgs[i])
		}
	}
	start := min(page*size, len(out))
	return out[start:min(start+size, len(out))], nil
}

func (m *memStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.msgs)
}

type fixture struct {
	cfg   *config.Config
	hub   *ws.Hub
	store *memStore
	srv   *httptest.Server
}

func newFixture(t *testing.T, burst int) *fixture {
	t.Helper()
	cfg := &config.Config{JWTSecret: "secret", RateLimitRPS: 0.0001, RateLimitBurst: burst, MaxBodyBytes: 1 << 10, RetryAttempts: 1}
	hub := ws.NewHub(ws.DefaultOptions())
	store := &memStore{}
	chat := service.NewChatService(store, hub, service.DefaultOptions(1))
	srv := httptest.NewServer(Routes(cfg, httpapi.NewHandler(chat, hub, cfg.MaxBodyBytes)))
	t.Cleanup(func() {
		srv.Close()
		_ = chat.Close(context.Background())
	})
	return &fixture{cfg, hub, store, srv}
}

func (f *fixture) token(t *testing.T, user string) string {
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"id": user}).SignedString([]byte(f.cfg.JWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (f *fixture) do(t *testing.T, method, path, user, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if user != "" {
		req.Header.Set("Authorization", "Bearer "+f.token(t, user))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func (f *fixture) dial(t *testing.T, user string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.srv.URL, "http")+"/ws", http.Header{"Authorization": {"Bearer " + f.token(t, user)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	deadline := time.Now().Add(5 * time.Second)
	for f.hub.ConnectionCounts([]string{user})[user] == 0 {
		if time.Now().After(deadline) {
			t.Fatal("connection never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return conn
}

func TestChatFlow(t *testing.T) {
	f := newFixture(t, 100)
	bob := f.dial(t, "bob")
	alice := f.dial(t, "alice")

	status, body := f.do(t, http.MethodPost, "/api/messages", "alice", `{"participants":["bob","alice"],"content":"hi bob"}`)
	if status != http.StatusAccepted {
		t.Fatalf("send: %d %s", status, body)
	}
	_ = bob.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := bob.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var msg models.Message
	_ = json.Unmarshal(data, &msg)
	if msg.Sender != "alice" || msg.Content != "hi bob" || !slices.Equal(msg.Participants, []string{"alice", "bob"}) {
		t.Errorf("bob received %s", data)
	}

	_ = alice.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, data, err := alice.ReadMessage(); err == nil {
		t.Errorf("sender received her own message: %s", data)
	}

	deadline := time.Now().Add(5 * time.Second)
	for f.store.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	status, body = f.do(t, http.MethodGet, "/api/messages/get?participants=alice,bob", "bob", "")
	if status != http.StatusOK || !strings.Contains(body, msg.ID.Hex()) {
		t.Errorf("history: %d %s", status, body)
	}

	status, body = f.do(t, http.MethodPost, "/api/connections", "", `{"users":["bob","carol"]}`)
	if status != http.StatusOK || strings.TrimSpace(body) != `{"bob":1,"carol":0}` {
		t.Errorf("connections: %d %s", status, body)
	}
}

func TestRouteMatrix(t *testing.T) {
	f := newFixture(t, 1000)
	tests := []struct {
		method, path, user, body string
		want                     int
	}{
		{http.MethodGet, "/health", "", "", 200},
		{http.MethodHead, "/health", "", "", 200},
		{http.MethodPost, "/health", "", "", 405},
		{http.MethodGet, "/ws", "", "", 401},
		{http.MethodGet, "/ws", "alice", "", 400},
		{http.MethodPost, "/ws", "alice", "", 405},
		{http.MethodPost, "/api/messages", "", `{}`, 401},
		{http.MethodGet, "/api/messages", "alice", "", 405},
		{http.MethodPut, "/api/messages", "alice", "", 405},
		{http.MethodPost, "/api/messages", "alice", `{"participants":["alice"],"content":"x"}`, 202},
		{http.MethodPost, "/api/messages", "alice", `{"participants":["bob"],"content":"x"}`, 403},
		{http.MethodGet, "/api/messages/get?participants=alice", "", "", 401},
		{http.MethodPost, "/api/messages/get?participants=alice", "alice", "", 405},
		{http.MethodGet, "/api/messages/get?participants=alice", "alice", "", 200},
		{http.MethodGet, "/api/messages/get?participants=bob", "alice", "", 403},
		{http.MethodGet, "/api/connections", "", "", 405},
		{http.MethodPost, "/api/connections", "", `{"users":["a"]}`, 200},
		{http.MethodGet, "/", "", "", 404},
		{http.MethodGet, "/api/", "alice", "", 404},
		{http.MethodGet, "/api/unknown", "alice", "", 404},
		{http.MethodGet, "/debug/pprof/", "", "", 404},
	}
	for _, tt := range tests {
		if status, body := f.do(t, tt.method, tt.path, tt.user, tt.body); status != tt.want {
			t.Errorf("%s %s as %q: %d, want %d (%s)", tt.method, tt.path, tt.user, status, tt.want, body)
		}
	}
}

func TestRateLimitAppliesToAPIButNotWebsocket(t *testing.T) {
	f := newFixture(t, 2)
	for i, want := range []int{202, 202, 429, 429} {
		if status, _ := f.do(t, http.MethodPost, "/api/messages", "alice", `{"participants":["alice"],"content":"x"}`); status != want {
			t.Errorf("request %d: %d, want %d", i, status, want)
		}
	}
	if status, _ := f.do(t, http.MethodGet, "/api/messages/get?participants=alice", "alice", ""); status != 429 {
		t.Errorf("history shares the user's budget: got %d", status)
	}
	if status, _ := f.do(t, http.MethodPost, "/api/messages", "bob", `{"participants":["bob"],"content":"x"}`); status != 202 {
		t.Errorf("another user was limited: %d", status)
	}
	f.dial(t, "alice")
}

func TestNewSetsHardenedTimeouts(t *testing.T) {
	srv := New(":1", http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 || srv.MaxHeaderBytes <= 0 {
		t.Errorf("server not hardened: %+v", srv)
	}
}

func TestPprofServer(t *testing.T) {
	ts := httptest.NewServer(Pprof("127.0.0.1:0").Handler)
	defer ts.Close()
	for path, want := range map[string]int{
		"/debug/pprof/":                  200,
		"/debug/pprof/goroutine?debug=1": 200,
		"/debug/pprof/heap?debug=1":      200,
		"/debug/pprof/cmdline":           200,
		"/health":                        404,
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
}
