package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"chat-microservice/internal/middleware"
	"chat-microservice/internal/service"
	"chat-microservice/pkg/models"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"go.mongodb.org/mongo-driver/v2/bson"
)

const secret = "secret"

type sendCall struct {
	sender       string
	participants []string
	content      string
}

type historyCall struct {
	user         string
	participants []string
	page, size   int
}

type fakeChat struct {
	mu       sync.Mutex
	sends    []sendCall
	history  []historyCall
	err      error
	messages []*models.Message
	id       bson.ObjectID
}

func (f *fakeChat) Send(sender string, participants []string, content string) (*models.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, sendCall{sender, participants, content})
	if f.err != nil {
		return nil, f.err
	}
	return &models.Message{ID: f.id}, nil
}

func (f *fakeChat) History(_ context.Context, user string, participants []string, page, size int) ([]*models.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.history = append(f.history, historyCall{user, participants, page, size})
	return f.messages, f.err
}

type fakeConns struct {
	mu     sync.Mutex
	served []string
	counts map[string]int
	asked  []string
}

func (f *fakeConns) Serve(conn *websocket.Conn, userID string) {
	f.mu.Lock()
	f.served = append(f.served, userID)
	f.mu.Unlock()
	_ = conn.Close()
}

func (f *fakeConns) ConnectionCounts(users []string) map[string]int {
	f.asked = users
	return f.counts
}

func token(t *testing.T, userID string) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"id": userID}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func call(t *testing.T, handler http.HandlerFunc, method, target, user, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if user != "" {
		req.Header.Set("Authorization", "Bearer "+token(t, user))
	}
	rec := httptest.NewRecorder()
	middleware.NewAuthMiddleware(secret).Verify(handler).ServeHTTP(rec, req)
	return rec
}

func TestSendMessageAccepted(t *testing.T) {
	chat := &fakeChat{id: bson.NewObjectID()}
	h := NewHandler(chat, &fakeConns{}, 1<<10)

	rec := call(t, h.HandleSendMessage, http.MethodPost, "/api/messages", "alice", `{"participants":["bob","alice"],"content":"hi","extra":1}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "message queued" || body["id"] != chat.id.Hex() {
		t.Errorf("body = %v", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	want := sendCall{"alice", []string{"bob", "alice"}, "hi"}
	if len(chat.sends) != 1 || chat.sends[0].sender != want.sender || !slices.Equal(chat.sends[0].participants, want.participants) || chat.sends[0].content != want.content {
		t.Errorf("sends = %+v", chat.sends)
	}
}

func TestSendMessageUsesAuthenticatedSenderOnly(t *testing.T) {
	chat := &fakeChat{}
	h := NewHandler(chat, &fakeConns{}, 1<<10)
	call(t, h.HandleSendMessage, http.MethodPost, "/api/messages", "alice", `{"sender":"mallory","participants":["alice"],"content":"hi"}`)
	if chat.sends[0].sender != "alice" {
		t.Errorf("sender = %q; the body must not choose the sender", chat.sends[0].sender)
	}
}

func TestSendMessageErrorMapping(t *testing.T) {
	tests := []struct {
		err    error
		status int
		body   string
	}{
		{models.ErrNoParticipants, 400, "participants array is required"},
		{models.ErrEmptyParticipant, 400, "participant IDs must not be empty"},
		{models.ErrTooManyParticipants, 400, "too many participants"},
		{service.ErrEmptyContent, 400, "content is required"},
		{service.ErrNotParticipant, 403, "forbidden"},
		{service.ErrOverloaded, 503, "service unavailable"},
		{service.ErrClosed, 503, "service unavailable"},
		{errors.New("mongo: connection refused at 10.0.0.1"), 500, "internal error"},
	}
	for _, tt := range tests {
		h := NewHandler(&fakeChat{err: tt.err}, &fakeConns{}, 1<<10)
		rec := call(t, h.HandleSendMessage, http.MethodPost, "/api/messages", "alice", `{"participants":["alice"],"content":"x"}`)
		if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.body) {
			t.Errorf("%v: status %d body %q, want %d %q", tt.err, rec.Code, rec.Body, tt.status, tt.body)
		}
		if tt.status == 503 && rec.Header().Get("Retry-After") == "" {
			t.Errorf("%v: 503 without Retry-After", tt.err)
		}
		if strings.Contains(rec.Body.String(), "10.0.0.1") {
			t.Errorf("internal error details leaked: %q", rec.Body)
		}
	}
}

func TestSendMessageRejectsMalformedBodies(t *testing.T) {
	bodies := map[string]int{
		"":                                       400,
		"{":                                      400,
		"null":                                   0,
		`[]`:                                     400,
		`"text"`:                                 400,
		`{"participants":"alice","content":"x"}`: 400,
		`{"participants":[1,2],"content":"x"}`:   400,
		`{"participants":["a"],"content":5}`:     400,
		`{"participants":["a"],"content":"x"}{}`: 400,
		`{"participants":["a"],"content":"x"} garbage`:                         400,
		`{"participants":["a"],"content":"` + strings.Repeat("x", 2048) + `"}`: 413,
	}
	for body, want := range bodies {
		chat := &fakeChat{}
		h := NewHandler(chat, &fakeConns{}, 1<<10)
		rec := call(t, h.HandleSendMessage, http.MethodPost, "/api/messages", "alice", body)
		if want == 0 {
			continue
		}
		if rec.Code != want {
			t.Errorf("body %.40q: status = %d, want %d", body, rec.Code, want)
		}
		if len(chat.sends) != 0 {
			t.Errorf("body %.40q reached the service", body)
		}
	}
}

func TestGetMessages(t *testing.T) {
	stored := []*models.Message{{Content: "a"}}
	tests := []struct {
		query string
		want  historyCall
	}{
		{"participants=alice,bob", historyCall{"alice", []string{"alice", "bob"}, 0, 50}},
		{"participants=bob,%20alice&page=3&size=20", historyCall{"alice", []string{"bob", " alice"}, 3, 20}},
		{"participants=alice&size=1000", historyCall{"alice", []string{"alice"}, 0, 100}},
		{"participants=alice&size=0", historyCall{"alice", []string{"alice"}, 0, 50}},
		{"participants=alice&size=-5", historyCall{"alice", []string{"alice"}, 0, 50}},
		{"participants=alice&size=abc", historyCall{"alice", []string{"alice"}, 0, 50}},
		{"participants=alice&page=-1", historyCall{"alice", []string{"alice"}, 0, 50}},
		{"participants=alice&page=x", historyCall{"alice", []string{"alice"}, 0, 50}},
		{"participants=alice&page=10000", historyCall{"alice", []string{"alice"}, 10000, 50}},
		{"participants=alice,,bob", historyCall{"alice", []string{"alice", "", "bob"}, 0, 50}},
	}
	for _, tt := range tests {
		chat := &fakeChat{messages: stored}
		h := NewHandler(chat, &fakeConns{}, 1<<10)
		rec := call(t, h.HandleGetMessages, http.MethodGet, "/api/messages/get?"+tt.query, "alice", "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", tt.query, rec.Code)
			continue
		}
		got := chat.history[0]
		if got.user != tt.want.user || !slices.Equal(got.participants, tt.want.participants) || got.page != tt.want.page || got.size != tt.want.size {
			t.Errorf("%s: call %+v, want %+v", tt.query, got, tt.want)
		}
		var decoded []models.Message
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil || len(decoded) != 1 {
			t.Errorf("%s: body %q", tt.query, rec.Body)
		}
	}
}

func TestGetMessagesRejects(t *testing.T) {
	tests := []struct {
		query  string
		err    error
		status int
	}{
		{"", nil, 400},
		{"participants=", nil, 400},
		{"page=1", nil, 400},
		{"participants=alice&page=10001", nil, 400},
		{"participants=alice&page=99999999999999999999", nil, 200},
		{"participants=bob", service.ErrNotParticipant, 403},
		{"participants=alice,", models.ErrEmptyParticipant, 400},
		{"participants=alice", errors.New("db down"), 500},
	}
	for _, tt := range tests {
		h := NewHandler(&fakeChat{err: tt.err, messages: []*models.Message{}}, &fakeConns{}, 1<<10)
		rec := call(t, h.HandleGetMessages, http.MethodGet, "/api/messages/get?"+tt.query, "alice", "")
		if rec.Code != tt.status {
			t.Errorf("%q: status = %d, want %d (%q)", tt.query, rec.Code, tt.status, rec.Body)
		}
	}
}

func TestGetMessagesEmptyIsArray(t *testing.T) {
	h := NewHandler(&fakeChat{messages: []*models.Message{}}, &fakeConns{}, 1<<10)
	rec := call(t, h.HandleGetMessages, http.MethodGet, "/api/messages/get?participants=alice", "alice", "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("body = %q, want []", rec.Body)
	}
}

func TestGetUserConnections(t *testing.T) {
	conns := &fakeConns{counts: map[string]int{"alice": 2, "bob": 0}}
	h := NewHandler(&fakeChat{}, conns, 1<<10)
	rec := httptest.NewRecorder()
	h.HandleGetUserConnections(rec, httptest.NewRequest(http.MethodPost, "/api/connections", strings.NewReader(`{"users":["alice","bob"]}`)))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"alice":2,"bob":0}` {
		t.Fatalf("status %d body %q", rec.Code, rec.Body)
	}
	if !slices.Equal(conns.asked, []string{"alice", "bob"}) {
		t.Errorf("asked = %q", conns.asked)
	}
}

func TestGetUserConnectionsRejects(t *testing.T) {
	tooMany, _ := json.Marshal(map[string]any{"users": make([]string, models.MaxParticipants+1)})
	bodies := map[string]int{
		``:                400,
		`{}`:              400,
		`{"users":[]}`:    400,
		`{"users":"bob"}`: 400,
		string(tooMany):   400,
		`{"users":["` + strings.Repeat("a", 4096) + `"]}`: 413,
	}
	for body, want := range bodies {
		h := NewHandler(&fakeChat{}, &fakeConns{}, 2<<10)
		if want == 400 && len(body) > 2<<10 {
			h = NewHandler(&fakeChat{}, &fakeConns{}, 1<<20)
		}
		rec := httptest.NewRecorder()
		h.HandleGetUserConnections(rec, httptest.NewRequest(http.MethodPost, "/api/connections", strings.NewReader(body)))
		if rec.Code != want {
			t.Errorf("body %.40q: status = %d, want %d", body, rec.Code, want)
		}
	}
}

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	NewHandler(&fakeChat{}, &fakeConns{}, 1).Health(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 || body["status"] != "ok" || body["time"] == "" {
		t.Errorf("status %d body %q", rec.Code, rec.Body)
	}
}

func TestWebsocketUpgradeServesAuthenticatedUser(t *testing.T) {
	conns := &fakeConns{}
	h := NewHandler(&fakeChat{}, conns, 1<<10)
	srv := httptest.NewServer(middleware.NewAuthMiddleware(secret).Verify(http.HandlerFunc(h.HandleWebsocket)))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), http.Header{
		"Authorization": {"Bearer " + token(t, "alice")},
		"Origin":        {"https://anything.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = conn.ReadMessage()
	_ = conn.Close()
	conns.mu.Lock()
	defer conns.mu.Unlock()
	if !slices.Equal(conns.served, []string{"alice"}) {
		t.Errorf("served = %q", conns.served)
	}
}

func TestWebsocketRejectsPlainHTTP(t *testing.T) {
	conns := &fakeConns{}
	h := NewHandler(&fakeChat{}, conns, 1<<10)
	rec := call(t, h.HandleWebsocket, http.MethodGet, "/ws", "alice", "")
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusBadRequest || len(conns.served) != 0 {
		t.Errorf("status %d body %q served %v", rec.Code, body, conns.served)
	}
}

func BenchmarkSendMessage(b *testing.B) {
	h := NewHandler(&fakeChat{}, &fakeConns{}, 1<<16)
	body := `{"participants":["alice","bob","carol"],"content":"hello there, how are you?"}`
	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/api/messages", strings.NewReader(body))
		h.HandleSendMessage(httptest.NewRecorder(), req)
	}
}
