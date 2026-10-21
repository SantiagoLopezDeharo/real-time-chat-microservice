//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

type chatMessage struct {
	ID           string    `json:"id"`
	Sender       string    `json:"sender"`
	Content      string    `json:"content"`
	CreatedAt    time.Time `json:"created_at"`
	Participants []string  `json:"participants"`
}

func signJWT(t testing.TB, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return token
}

// userToken mirrors demo/jwt.js: the user ID travels in the "id" claim.
func (s *server) userToken(t testing.TB, userID string) string {
	return signJWT(t, jwt.SigningMethodHS256, []byte(s.jwtSecret), jwt.MapClaims{
		"id":  userID,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
}

type response struct {
	status int
	body   string
	header http.Header
}

func (s *server) request(t testing.TB, method, path, token string, body []byte) response {
	t.Helper()
	req, err := http.NewRequest(method, s.baseURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return response{resp.StatusCode, string(data), resp.Header}
}

func sendBody(participants []string, content string) []byte {
	data, _ := json.Marshal(map[string]any{"participants": participants, "content": content})
	return data
}

func (s *server) send(t testing.TB, sender string, participants []string, content string) response {
	t.Helper()
	return s.request(t, http.MethodPost, "/api/messages", s.userToken(t, sender), sendBody(participants, content))
}

func (s *server) mustSend(t testing.TB, sender string, participants []string, content string) string {
	t.Helper()
	res := s.send(t, sender, participants, content)
	if res.status != http.StatusAccepted {
		t.Fatalf("send as %s: %d %s", sender, res.status, res.body)
	}
	var body struct{ ID string }
	_ = json.Unmarshal([]byte(res.body), &body)
	return body.ID
}

func historyPath(participants []string, page, size int) string {
	q := url.Values{"participants": {strings.Join(participants, ",")}}
	if page >= 0 {
		q.Set("page", strconv.Itoa(page))
	}
	if size >= 0 {
		q.Set("size", strconv.Itoa(size))
	}
	return "/api/messages/get?" + q.Encode()
}

func (s *server) history(t testing.TB, user string, participants []string, page, size int) []chatMessage {
	t.Helper()
	res := s.request(t, http.MethodGet, historyPath(participants, page, size), s.userToken(t, user), nil)
	if res.status != http.StatusOK {
		t.Fatalf("history as %s: %d %s", user, res.status, res.body)
	}
	var msgs []chatMessage
	if err := json.Unmarshal([]byte(res.body), &msgs); err != nil {
		t.Fatalf("history body %q: %v", res.body, err)
	}
	return msgs
}

// awaitPersisted polls the history until it holds n messages.
func (s *server) awaitPersisted(t testing.TB, user string, participants []string, n int, timeout time.Duration) []chatMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var all []chatMessage
	for {
		all = all[:0]
		for page := 0; ; page++ {
			batch := s.history(t, user, participants, page, 100)
			all = append(all, batch...)
			if len(batch) < 100 {
				break
			}
		}
		if len(all) >= n || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(all) != n {
		t.Fatalf("history holds %d messages, want %d", len(all), n)
	}
	return all
}

func (s *server) connectionCounts(t testing.TB, users []string) map[string]int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"users": users})
	res := s.request(t, http.MethodPost, "/api/connections", "", body)
	if res.status != http.StatusOK {
		t.Fatalf("connections: %d %s", res.status, res.body)
	}
	counts := map[string]int{}
	_ = json.Unmarshal([]byte(res.body), &counts)
	return counts
}

// awaitConnections waits until the server reports the expected number of live
// connections for every user, proving their registrations are complete.
func (s *server) awaitConnections(t testing.TB, want map[string]int) {
	t.Helper()
	users := make([]string, 0, len(want))
	for u := range want {
		users = append(users, u)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		ready := true
		for start := 0; start < len(users) && ready; start += 1000 {
			counts := s.connectionCounts(t, users[start:min(start+1000, len(users))])
			for _, u := range users[start:min(start+1000, len(users))] {
				if counts[u] != want[u] {
					ready = false
					break
				}
			}
		}
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("connections never reached the expected counts")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// tab is one websocket connection of a simulated user. Its reader only
// timestamps frames so it never becomes the bottleneck.
type tab struct {
	user   string
	conn   *websocket.Conn
	frames atomic.Int64

	mu       sync.Mutex
	inbox    []delivery
	notify   chan struct{}
	done     chan struct{}
	closeErr error
}

type delivery struct {
	data []byte
	at   time.Time
}

func (s *server) dial(token string, header http.Header) (*websocket.Conn, *http.Response, error) {
	if header == nil {
		header = http.Header{}
	}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	return dialer.Dial(s.wsURL+"/ws", header)
}

func (s *server) tryOpen(t testing.TB, user string) (*tab, error) {
	conn, resp, err := s.dial(s.userToken(t, user), nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		return nil, fmt.Errorf("open %s: %w (status %d)", user, err, status)
	}
	tb := &tab{user: user, conn: conn, notify: make(chan struct{}, 1), done: make(chan struct{})}
	go tb.read()
	t.Cleanup(func() { _ = conn.Close() })
	return tb, nil
}

// open connects a tab for user and waits until the server has registered it.
func (s *server) open(t testing.TB, user string) *tab {
	t.Helper()
	before := s.connectionCounts(t, []string{user})[user]
	tb, err := s.tryOpen(t, user)
	if err != nil {
		t.Fatal(err)
	}
	s.awaitConnections(t, map[string]int{user: before + 1})
	return tb
}

// openUsers connects one tab per user concurrently and waits for all of them.
func (s *server) openUsers(t testing.TB, users []string) []*tab {
	t.Helper()
	tabs := make([]*tab, len(users))
	errs := make([]error, len(users))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 64)
	for i, u := range users {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			tabs[i], errs[i] = s.tryOpen(t, u)
		})
	}
	wg.Wait()
	want := map[string]int{}
	for i, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
		want[users[i]]++
	}
	s.awaitConnections(t, want)
	return tabs
}

func names(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	return out
}

func (tb *tab) read() {
	defer close(tb.done)
	for {
		_, frame, err := tb.conn.ReadMessage()
		if err != nil {
			tb.mu.Lock()
			tb.closeErr = err
			tb.mu.Unlock()
			return
		}
		at := time.Now()
		tb.frames.Add(1)
		tb.mu.Lock()
		tb.inbox = append(tb.inbox, delivery{data: frame, at: at})
		tb.mu.Unlock()
		select {
		case tb.notify <- struct{}{}:
		default:
		}
	}
}

func (tb *tab) take(max int) []delivery {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	n := min(max, len(tb.inbox))
	out := tb.inbox[:n:n]
	tb.inbox = tb.inbox[n:]
	return out
}

func (tb *tab) err() error {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return tb.closeErr
}

func (tb *tab) deliveries(t testing.TB, n int, timeout time.Duration) []delivery {
	t.Helper()
	out := make([]delivery, 0, n)
	deadline := time.After(timeout)
	for len(out) < n {
		out = append(out, tb.take(n-len(out))...)
		if len(out) == n {
			break
		}
		select {
		case <-tb.notify:
		case <-tb.done:
			out = append(out, tb.take(n-len(out))...)
			if len(out) < n {
				t.Fatalf("%s's connection closed after %d/%d messages: %v", tb.user, len(out), n, tb.err())
			}
		case <-deadline:
			t.Fatalf("%s received %d/%d messages within %s", tb.user, len(out), n, timeout)
		}
	}
	return out
}

func (tb *tab) messages(t testing.TB, n int, timeout time.Duration) []chatMessage {
	t.Helper()
	ds := tb.deliveries(t, n, timeout)
	out := make([]chatMessage, len(ds))
	for i, d := range ds {
		if err := json.Unmarshal(d.data, &out[i]); err != nil {
			t.Fatalf("%s received a frame that is not one message: %q", tb.user, d.data)
		}
	}
	return out
}

func (tb *tab) next(t testing.TB, timeout time.Duration) chatMessage {
	t.Helper()
	return tb.messages(t, 1, timeout)[0]
}

func (tb *tab) expectNothing(t testing.TB, wait time.Duration) {
	t.Helper()
	time.Sleep(wait)
	if got := tb.take(1); len(got) > 0 {
		t.Fatalf("%s received an unexpected message: %s", tb.user, got[0].data)
	}
}

func (tb *tab) waitClosed(t testing.TB, timeout time.Duration) error {
	t.Helper()
	select {
	case <-tb.done:
		return tb.err()
	case <-time.After(timeout):
		t.Fatalf("%s's connection still open after %s", tb.user, timeout)
	}
	return nil
}
