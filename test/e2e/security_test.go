//go:build e2e

package e2e

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

func forgedTokens(t *testing.T, s *server) map[string]string {
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	key := []byte(s.jwtSecret)
	valid := s.userToken(t, "alice")
	parts := strings.Split(valid, ".")
	return map[string]string{
		"garbage":            "garbage",
		"wrong secret":       signJWT(t, jwt.SigningMethodHS256, []byte("guess"), jwt.MapClaims{"id": "alice"}),
		"alg none":           signJWT(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, jwt.MapClaims{"id": "alice"}),
		"rs256":              signJWT(t, jwt.SigningMethodRS256, rsaKey, jwt.MapClaims{"id": "alice"}),
		"es256":              signJWT(t, jwt.SigningMethodES256, ecKey, jwt.MapClaims{"id": "alice"}),
		"expired":            signJWT(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"id": "alice", "exp": time.Now().Add(-time.Second).Unix()}),
		"not yet valid":      signJWT(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"id": "alice", "nbf": time.Now().Add(time.Hour).Unix()}),
		"missing id":         signJWT(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"sub": "alice"}),
		"empty id":           signJWT(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"id": ""}),
		"numeric id":         signJWT(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"id": 7}),
		"tampered payload":   parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"id":"bob"}`)) + "." + parts[2],
		"stripped signature": parts[0] + "." + parts[1] + ".",
	}
}

func TestEndpointsRejectForgedTokens(t *testing.T) {
	s := startServer(t)
	bob := s.open(t, "bob")
	for name, token := range forgedTokens(t, s) {
		t.Run(name, func(t *testing.T) {
			if conn, resp, err := s.dial(token, nil); err == nil {
				_ = conn.Close()
				t.Error("websocket handshake succeeded")
			} else if resp == nil || resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("websocket: resp %v, want 401", resp)
			}
			if res := s.request(t, http.MethodPost, "/api/messages", token, sendBody([]string{"alice", "bob"}, "forged")); res.status != http.StatusUnauthorized {
				t.Errorf("send: %d", res.status)
			}
			if res := s.request(t, http.MethodGet, historyPath([]string{"alice", "bob"}, 0, 10), token, nil); res.status != http.StatusUnauthorized {
				t.Errorf("history: %d", res.status)
			}
		})
	}
	bob.expectNothing(t, 200*time.Millisecond)
}

func TestMalformedAuthorizationHeaders(t *testing.T) {
	s := startServer(t)
	valid := s.userToken(t, "alice")
	for _, header := range []string{"", valid, "Basic " + valid, "bearer " + valid, "Bearer", "Bearer ", "Bearer  " + valid, "Bearer " + valid + " x", "Token " + valid} {
		req, _ := http.NewRequest(http.MethodGet, s.baseURL+historyPath([]string{"alice"}, 0, 1), nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Authorization %.30q: %d", header, resp.StatusCode)
		}
	}
	if _, resp, err := websocket.DefaultDialer.Dial(s.wsURL+"/ws?access_token="+valid, nil); err == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Error("websocket with access_token authenticated")
	}
}

func TestWebsocketQueryToken(t *testing.T) {
	s := startServer(t)
	conn, _, err := websocket.DefaultDialer.Dial(s.wsURL+"/ws?token="+s.userToken(t, "alice"), nil)
	if err != nil {
		t.Fatalf("query token rejected: %v", err)
	}
	_ = conn.Close()

	for name, token := range forgedTokens(t, s) {
		if _, resp, err := websocket.DefaultDialer.Dial(s.wsURL+"/ws?token="+url.QueryEscape(token), nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: forged query token authenticated", name)
		}
	}

	valid := s.userToken(t, "alice")
	if res := s.request(t, http.MethodGet, historyPath([]string{"alice"}, 0, 1)+"&token="+valid, "", nil); res.status != http.StatusUnauthorized {
		t.Errorf("history with query token: %d, want 401", res.status)
	}
	if res := s.request(t, http.MethodPost, "/api/messages?token="+valid, "", sendBody([]string{"alice"}, "x")); res.status != http.StatusUnauthorized {
		t.Errorf("send with query token: %d, want 401", res.status)
	}
}

func TestSenderCannotBeSpoofed(t *testing.T) {
	s := startServer(t)
	bob := s.open(t, "bob")
	body, _ := json.Marshal(map[string]any{"sender": "admin", "participants": []string{"mallory", "bob"}, "content": "trust me"})
	if res := s.request(t, http.MethodPost, "/api/messages", s.userToken(t, "mallory"), body); res.status != http.StatusAccepted {
		t.Fatalf("send: %d %s", res.status, res.body)
	}
	if got := bob.next(t, deliveryTimeout); got.Sender != "mallory" {
		t.Fatalf("sender = %q; the body chose the sender", got.Sender)
	}
}

func TestOutsidersCannotWriteOrReadConversations(t *testing.T) {
	s := startServer(t)
	alice, bob, mallory := s.open(t, "alice"), s.open(t, "bob"), s.open(t, "mallory")
	s.mustSend(t, "alice", []string{"alice", "bob"}, "private")
	bob.next(t, deliveryTimeout)
	s.awaitPersisted(t, "alice", []string{"alice", "bob"}, 1, deliveryTimeout)

	if res := s.send(t, "mallory", []string{"alice", "bob"}, "injected"); res.status != http.StatusForbidden {
		t.Errorf("outsider send: %d", res.status)
	}
	for _, channel := range [][]string{{"alice", "bob"}, {"bob", "alice"}, {"alice", "bob", "bob"}} {
		res := s.request(t, http.MethodGet, historyPath(channel, 0, 10), s.userToken(t, "mallory"), nil)
		if res.status != http.StatusForbidden || strings.Contains(res.body, "private") {
			t.Errorf("outsider history %q: %d %s", channel, res.status, res.body)
		}
	}
	if msgs := s.history(t, "mallory", []string{"alice", "bob", "mallory"}, 0, 10); len(msgs) != 0 {
		t.Errorf("joining the channel leaked %d messages from the pair", len(msgs))
	}
	alice.expectNothing(t, 200*time.Millisecond)
	bob.expectNothing(t, 0)
	mallory.expectNothing(t, 0)
}

func TestQueryInjectionIsInert(t *testing.T) {
	s := startServer(t)
	s.mustSend(t, "alice", []string{"alice", "bob"}, "secret")
	s.awaitPersisted(t, "alice", []string{"alice", "bob"}, 1, deliveryTimeout)

	for _, body := range []string{
		`{"participants":{"$ne":null},"content":"x"}`,
		`{"participants":[{"$gt":""}],"content":"x"}`,
		`{"participants":["mallory"],"content":{"$where":"1"}}`,
	} {
		if res := s.request(t, http.MethodPost, "/api/messages", s.userToken(t, "mallory"), []byte(body)); res.status != http.StatusBadRequest {
			t.Errorf("%s: %d", body, res.status)
		}
	}
	for _, raw := range []string{`{"$ne":null}`, `mallory,{"$gt":""}`, `mallory,$where`} {
		res := s.request(t, http.MethodGet, "/api/messages/get?participants="+url.QueryEscape(raw), s.userToken(t, "mallory"), nil)
		if strings.Contains(res.body, "secret") {
			t.Errorf("participants=%s leaked other channels: %s", raw, res.body)
		}
	}
}

func TestRequestLimits(t *testing.T) {
	s := startServer(t, withEnv("MAX_BODY_BYTES", "4096"))
	token := s.userToken(t, "alice")
	tooMany := names("u", 1001)
	tooMany[0] = "alice"
	tests := []struct {
		name string
		path string
		body string
		want int
	}{
		{"oversized message", "/api/messages", string(sendBody([]string{"alice", "bob"}, strings.Repeat("x", 5000))), 413},
		{"too many participants", "/api/messages", string(mustJSON(map[string]any{"participants": tooMany, "content": "x"})), 413},
		{"empty participant", "/api/messages", string(sendBody([]string{"alice", ""}, "x")), 400},
		{"no participants", "/api/messages", string(sendBody([]string{}, "x")), 400},
		{"empty content", "/api/messages", string(sendBody([]string{"alice", "bob"}, "  ")), 400},
		{"invalid json", "/api/messages", `{"participants":`, 400},
		{"two documents", "/api/messages", string(sendBody([]string{"alice"}, "a")) + string(sendBody([]string{"alice"}, "b")), 400},
		{"oversized connections query", "/api/connections", string(mustJSON(map[string]any{"users": names("user-with-a-long-name", 400)})), 413},
	}
	for _, tt := range tests {
		res := s.request(t, http.MethodPost, tt.path, token, []byte(tt.body))
		if res.status != tt.want {
			t.Errorf("%s: %d, want %d (%s)", tt.name, res.status, tt.want, res.body)
		}
		assertNoLeak(t, s, res.body)
	}

	big := startServer(t, withEnv("MAX_BODY_BYTES", "1048576"))
	for path, body := range map[string]any{
		"/api/messages":    map[string]any{"participants": tooMany, "content": "x"},
		"/api/connections": map[string]any{"users": names("u", 1001)},
	} {
		if res := big.request(t, http.MethodPost, path, big.userToken(t, "alice"), mustJSON(body)); res.status != http.StatusBadRequest {
			t.Errorf("%s with 1001 users: %d", path, res.status)
		}
	}
	if res := big.request(t, http.MethodGet, historyPath([]string{"alice"}, 10_001, 10), big.userToken(t, "alice"), nil); res.status != http.StatusBadRequest {
		t.Errorf("deep page: %d", res.status)
	}
}

func mustJSON(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

func TestRateLimiting(t *testing.T) {
	s := startServer(t, withEnv("RATE_LIMIT_RPS", "0.01"), withEnv("RATE_LIMIT_BURST", "3"))
	var statuses []int
	for range 5 {
		statuses = append(statuses, s.send(t, "alice", []string{"alice", "bob"}, "spam").status)
	}
	if want := []int{202, 202, 202, 429, 429}; !slices.Equal(statuses, want) {
		t.Errorf("statuses = %v, want %v", statuses, want)
	}
	if res := s.request(t, http.MethodGet, historyPath([]string{"alice"}, 0, 1), s.userToken(t, "alice"), nil); res.status != http.StatusTooManyRequests {
		t.Errorf("history shares the budget: %d", res.status)
	}
	if res := s.send(t, "bob", []string{"alice", "bob"}, "hi"); res.status != http.StatusAccepted {
		t.Errorf("other users are unaffected: %d", res.status)
	}
	s.open(t, "alice")
}

func TestMethodAndRouteHardening(t *testing.T) {
	s := startServer(t)
	token := s.userToken(t, "alice")
	tests := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/messages", 405},
		{http.MethodDelete, "/api/messages", 405},
		{http.MethodPost, "/api/messages/get", 405},
		{http.MethodGet, "/api/connections", 405},
		{http.MethodPost, "/ws", 405},
		{http.MethodPost, "/health", 405},
		{"TRACE", "/api/messages", 405},
		{http.MethodGet, "/", 404},
		{http.MethodGet, "/api/", 404},
		{http.MethodGet, "/api/messages/get/", 404},
		{http.MethodGet, "/debug/pprof/", 404},
		{http.MethodGet, "/.env", 404},
		{http.MethodGet, "/ws", 400},
	}
	for _, tt := range tests {
		res := s.request(t, tt.method, tt.path, token, nil)
		if res.status != tt.want {
			t.Errorf("%s %s: %d, want %d", tt.method, tt.path, res.status, tt.want)
		}
		assertNoLeak(t, s, res.body)
	}
}

func TestOversizedClientFrameClosesOnlyThatConnection(t *testing.T) {
	s := startServer(t)
	mallory, bob := s.open(t, "mallory"), s.open(t, "bob")
	_ = mallory.conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("A", 64<<10)))
	if err := mallory.waitClosed(t, 5*time.Second); !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		t.Errorf("close = %v, want 1009", err)
	}
	s.mustSend(t, "alice", []string{"alice", "bob"}, "ok")
	if got := bob.next(t, deliveryTimeout); got.Content != "ok" {
		t.Fatalf("bob got %+v", got)
	}
}

func TestProtocolViolationsAreRejected(t *testing.T) {
	s := startServer(t)

	t.Run("unmasked client frame", func(t *testing.T) {
		conn, reader := rawHandshake(t, s, s.userToken(t, "mallory"))
		_, _ = conn.Write([]byte{0x81, 0x05, 'h', 'e', 'l', 'l', 'o'})
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		data, _ := io.ReadAll(reader)
		if len(data) < 4 || data[0] != 0x88 || int(data[2])<<8|int(data[3]) != websocket.CloseProtocolError {
			t.Fatalf("expected close 1002, got % x", data)
		}
	})
	t.Run("garbage bytes", func(t *testing.T) {
		conn, err := net.Dial("tcp", s.addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("\x00\x01\xff garbage \r\n\r\n"))
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if line, _ := bufio.NewReader(conn).ReadString('\n'); !strings.Contains(line, "400") {
			t.Fatalf("response %q", line)
		}
	})
	t.Run("oversized headers", func(t *testing.T) {
		if line := rawRequest(t, s, "GET /health HTTP/1.1\r\nHost: x\r\nX-Big: "+strings.Repeat("a", 128<<10)+"\r\n\r\n"); !strings.Contains(line, " 431 ") {
			t.Fatalf("status line %q", line)
		}
	})
	if res := s.request(t, http.MethodGet, "/health", "", nil); res.status != 200 {
		t.Fatalf("unhealthy after abuse: %d", res.status)
	}
}

func TestSlowlorisConnectionsAreCut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the header read timeout")
	}
	s := startServer(t)
	conn, err := net.Dial("tcp", s.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("GET /health HTTP/1.1\r\nHost: x\r\n"))
	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	_, _ = io.ReadAll(conn)
	if elapsed := time.Since(start); elapsed > 12*time.Second {
		t.Fatalf("incomplete request held the connection for %s", elapsed)
	}
}

func TestConnectionFloodDoesNotLeak(t *testing.T) {
	s := startServer(t)
	baseline := s.goroutines(t)
	for wave := range 5 {
		tabs := s.openUsers(t, names(string(rune('a'+wave)), 200))
		for i, tb := range tabs {
			if i%2 == 0 {
				_ = tb.conn.UnderlyingConn().Close()
			} else {
				_ = tb.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				_ = tb.conn.Close()
			}
		}
	}
	for range 200 {
		if conn, err := net.Dial("tcp", s.addr); err == nil {
			_, _ = conn.Write([]byte("GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\n"))
			_ = conn.Close()
		}
	}
	if after := s.waitGoroutinesBelow(t, baseline+5, 20*time.Second); after > baseline+5 {
		t.Fatalf("goroutines grew from %d to %d", baseline, after)
	}
}

var leakPattern = regexp.MustCompile(`goroutine|\.go:\d+|panic|runtime\.|mongodb://|/Users/|/home/`)

func assertNoLeak(t *testing.T, s *server, body string) {
	t.Helper()
	if strings.Contains(body, s.jwtSecret) || leakPattern.MatchString(body) {
		t.Errorf("response leaks internals: %q", body)
	}
}

func rawHandshake(t *testing.T, s *server, token string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", s.addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, _ = conn.Write([]byte("GET /ws HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + token + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"))
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake: %v %v", err, resp)
	}
	return conn, reader
}

func rawRequest(t *testing.T, s *server, request string) string {
	t.Helper()
	conn, err := net.Dial("tcp", s.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(request))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, _ := bufio.NewReader(conn).ReadString('\n')
	return line
}
