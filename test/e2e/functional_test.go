//go:build e2e

package e2e

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const deliveryTimeout = 10 * time.Second

func TestHealth(t *testing.T) {
	s := startServer(t)
	res := s.request(t, http.MethodGet, "/health", "", nil)
	var body map[string]string
	if err := json.Unmarshal([]byte(res.body), &body); err != nil || res.status != 200 || body["status"] != "ok" {
		t.Fatalf("health: %d %s", res.status, res.body)
	}
}

func TestDirectMessage(t *testing.T) {
	s := startServer(t)
	alice, bob := s.open(t, "alice"), s.open(t, "bob")

	id := s.mustSend(t, "alice", []string{"alice", "bob"}, "hi bob")
	got := bob.next(t, deliveryTimeout)
	if got.ID != id || got.Sender != "alice" || got.Content != "hi bob" || !slices.Equal(got.Participants, []string{"alice", "bob"}) {
		t.Fatalf("bob received %+v", got)
	}
	if time.Since(got.CreatedAt) > time.Minute || got.CreatedAt.After(time.Now().Add(time.Second)) {
		t.Errorf("created_at = %v", got.CreatedAt)
	}
	alice.expectNothing(t, 200*time.Millisecond)

	stored := s.awaitPersisted(t, "bob", []string{"alice", "bob"}, 1, deliveryTimeout)
	if stored[0].ID != got.ID || !stored[0].CreatedAt.Equal(got.CreatedAt) || stored[0].Content != got.Content {
		t.Errorf("stored %+v differs from delivered %+v", stored[0], got)
	}
}

func TestGroupFanOutInOrder(t *testing.T) {
	s := startServer(t)
	members := names("member", 40)
	tabs := s.openUsers(t, members)
	group := append([]string{"host"}, members...)
	host := s.open(t, "host")

	const messages = 20
	for i := range messages {
		s.mustSend(t, "host", group, fmt.Sprintf("announcement %d", i))
	}
	for _, tb := range tabs {
		for i, m := range tb.messages(t, messages, deliveryTimeout) {
			if m.Content != fmt.Sprintf("announcement %d", i) || m.Sender != "host" || len(m.Participants) != len(group) {
				t.Fatalf("%s message %d = %+v", tb.user, i, m)
			}
		}
		tb.expectNothing(t, 0)
	}
	host.expectNothing(t, 100*time.Millisecond)
}

func TestEveryTabOfARecipientReceives(t *testing.T) {
	s := startServer(t)
	var tabs []*tab
	for range 4 {
		tabs = append(tabs, s.open(t, "bob"))
	}
	s.mustSend(t, "alice", []string{"alice", "bob"}, "to all of bob's tabs")
	for i, tb := range tabs {
		if got := tb.next(t, deliveryTimeout); got.Content != "to all of bob's tabs" {
			t.Errorf("tab %d got %+v", i, got)
		}
	}
	if counts := s.connectionCounts(t, []string{"bob", "nobody"}); counts["bob"] != 4 || counts["nobody"] != 0 {
		t.Errorf("connection counts = %v", counts)
	}
}

func TestOneConnectionReceivesEveryConversation(t *testing.T) {
	s := startServer(t)
	alice := s.open(t, "alice")
	conversations := [][]string{{"alice", "bob"}, {"alice", "carol"}, {"alice", "bob", "carol", "dave"}}
	senders := []string{"bob", "carol", "dave"}
	for i, c := range conversations {
		s.mustSend(t, senders[i], c, fmt.Sprint("conversation ", i))
	}
	for i, m := range alice.messages(t, len(conversations), deliveryTimeout) {
		if m.Content != fmt.Sprint("conversation ", i) || !slices.Equal(m.Participants, conversations[i]) {
			t.Errorf("message %d = %+v", i, m)
		}
	}
}

func TestParticipantsAreAnUnorderedSet(t *testing.T) {
	s := startServer(t)
	bob := s.open(t, "bob")
	s.mustSend(t, "alice", []string{"bob", "alice", "bob", " alice "}, "dedup")

	got := bob.next(t, deliveryTimeout)
	if !slices.Equal(got.Participants, []string{"alice", "bob"}) {
		t.Errorf("participants = %q", got.Participants)
	}
	bob.expectNothing(t, 200*time.Millisecond)
	if frames := bob.frames.Load(); frames != 1 {
		t.Errorf("bob received %d frames for one message", frames)
	}

	for _, order := range [][]string{{"alice", "bob"}, {"bob", "alice"}, {"bob", "alice", "bob"}} {
		if msgs := s.awaitPersisted(t, "alice", order, 1, deliveryTimeout); msgs[0].Content != "dedup" {
			t.Errorf("history for %q = %+v", order, msgs)
		}
	}
	if msgs := s.history(t, "alice", []string{"alice", "bob", "carol"}, 0, 10); len(msgs) != 0 {
		t.Errorf("a different channel returned %d messages", len(msgs))
	}
}

func TestHistoryPaginationNewestFirst(t *testing.T) {
	s := startServer(t)
	channel := []string{"alice", "bob"}
	for i := range 130 {
		s.mustSend(t, []string{"alice", "bob"}[i%2], channel, fmt.Sprintf("m%03d", i))
	}
	all := s.awaitPersisted(t, "alice", channel, 130, deliveryTimeout)
	for i, m := range all {
		if want := fmt.Sprintf("m%03d", 129-i); m.Content != want {
			t.Fatalf("position %d = %s, want %s", i, m.Content, want)
		}
	}

	if got := s.history(t, "bob", channel, -1, -1); len(got) != 50 || got[0].Content != "m129" {
		t.Errorf("default page = %d messages starting %q", len(got), got[0].Content)
	}
	if got := s.history(t, "bob", channel, 0, 1000); len(got) != 100 {
		t.Errorf("size is capped at 100, got %d", len(got))
	}
	if got := s.history(t, "bob", channel, 2, 50); len(got) != 30 || got[29].Content != "m000" {
		t.Errorf("last page = %d messages", len(got))
	}
	if got := s.history(t, "bob", channel, 99, 50); len(got) != 0 {
		t.Errorf("page past the end = %d messages", len(got))
	}
	if res := s.request(t, http.MethodGet, historyPath([]string{"alice", "nobody"}, 0, 10), s.userToken(t, "alice"), nil); strings.TrimSpace(res.body) != "[]" {
		t.Errorf("empty channel body = %q, want []", res.body)
	}
}

func TestContentIsDeliveredAndStoredVerbatim(t *testing.T) {
	s := startServer(t, withEnv("MAX_BODY_BYTES", "65536"))
	bob := s.open(t, "bob")
	contents := []string{
		"plain",
		"  leading and trailing spaces  ",
		"日本語 – émoji 🚀 – עברית",
		"<script>alert(document.cookie)</script>",
		`{"$where":"sleep(1000)"}`,
		"line\nbreaks\tand\\backslashes\"quotes\"",
		strings.Repeat("long ", 10_000),
	}
	for _, c := range contents {
		s.mustSend(t, "alice", []string{"alice", "bob"}, c)
	}
	for i, m := range bob.messages(t, len(contents), deliveryTimeout) {
		if m.Content != contents[i] {
			t.Errorf("delivered content %d altered: %.60q", i, m.Content)
		}
	}
	stored := s.awaitPersisted(t, "alice", []string{"alice", "bob"}, len(contents), deliveryTimeout)
	for i, m := range stored {
		if want := contents[len(contents)-1-i]; m.Content != want {
			t.Errorf("stored content %d altered: %.60q", i, m.Content)
		}
	}
}

func TestNoteToSelf(t *testing.T) {
	s := startServer(t)
	alice := s.open(t, "alice")
	s.mustSend(t, "alice", []string{"alice"}, "remember the milk")
	alice.expectNothing(t, 200*time.Millisecond)
	if msgs := s.awaitPersisted(t, "alice", []string{"alice"}, 1, deliveryTimeout); msgs[0].Content != "remember the milk" {
		t.Errorf("history = %+v", msgs)
	}
}

func TestReconnectDoesNotReplay(t *testing.T) {
	s := startServer(t)
	first := s.open(t, "bob")
	s.mustSend(t, "alice", []string{"alice", "bob"}, "one")
	first.next(t, deliveryTimeout)
	_ = first.conn.Close()
	s.awaitConnections(t, map[string]int{"bob": 0})

	s.mustSend(t, "alice", []string{"alice", "bob"}, "missed while offline")
	second := s.open(t, "bob")
	s.mustSend(t, "alice", []string{"alice", "bob"}, "three")
	if got := second.next(t, deliveryTimeout); got.Content != "three" {
		t.Fatalf("reconnected tab got %q; offline messages belong to history only", got.Content)
	}
	if msgs := s.awaitPersisted(t, "bob", []string{"alice", "bob"}, 3, deliveryTimeout); msgs[1].Content != "missed while offline" {
		t.Errorf("offline message not in history: %+v", msgs)
	}
}

func TestClientFramesAreIgnored(t *testing.T) {
	s := startServer(t)
	mallory, bob := s.open(t, "mallory"), s.open(t, "bob")
	for range 20 {
		_ = mallory.conn.WriteMessage(websocket.TextMessage, []byte(`{"participants":["mallory","bob"],"content":"spoof"}`))
	}
	time.Sleep(200 * time.Millisecond)
	s.mustSend(t, "alice", []string{"alice", "bob"}, "legit")
	if got := bob.next(t, deliveryTimeout); got.Content != "legit" {
		t.Fatalf("bob's first message %q: client frames were relayed", got.Content)
	}
	mallory.expectNothing(t, 0)
}

func TestConnectionOutlivesHTTPTimeouts(t *testing.T) {
	if testing.Short() {
		t.Skip("waits past the HTTP server timeouts")
	}
	s := startServer(t)
	bob := s.open(t, "bob")
	time.Sleep(17 * time.Second)
	s.mustSend(t, "alice", []string{"alice", "bob"}, "still there?")
	if got := bob.next(t, deliveryTimeout); got.Content != "still there?" {
		t.Fatalf("got %+v", got)
	}
}

func TestGracefulShutdownClosesClients(t *testing.T) {
	s := startServer(t)
	tabs := s.openUsers(t, names("user", 20))

	start := time.Now()
	if err := s.stop(20 * time.Second); err != nil {
		t.Fatalf("exit: %v\n%s", err, s.logs)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("shutdown took %s", elapsed)
	}
	for _, tb := range tabs {
		if err := tb.waitClosed(t, 5*time.Second); !websocket.IsCloseError(err, websocket.CloseGoingAway) {
			t.Errorf("%s close = %v, want 1001", tb.user, err)
		}
	}
	if conn, err := net.DialTimeout("tcp", s.addr, time.Second); err == nil {
		_ = conn.Close()
		t.Error("port still accepting connections")
	}
}

func TestShutdownPersistsEveryAcknowledgedMessage(t *testing.T) {
	db := "e2e_drain_" + randomSecret()[:8]
	s := startServer(t, withDatabase(db))
	channel := []string{"alice", "bob"}

	const burst = 400
	for i := range burst {
		s.mustSend(t, "alice", channel, fmt.Sprint("msg ", i))
	}
	if err := s.stop(20 * time.Second); err != nil {
		t.Fatalf("exit: %v", err)
	}

	restarted := startServer(t, withDatabase(db))
	stored := restarted.awaitPersisted(t, "bob", channel, burst, 5*time.Second)
	if stored[0].Content != fmt.Sprint("msg ", burst-1) {
		t.Errorf("newest stored message = %q", stored[0].Content)
	}
}

func TestStartupRejectsInvalidConfiguration(t *testing.T) {
	tests := map[string][]option{
		"missing JWT secret":   {withoutEnv("JWT_SECRET")},
		"zero retries":         {withEnv("RETRY_ATTEMPTS", "0")},
		"invalid burst":        {withEnv("RATE_LIMIT_BURST", "lots")},
		"invalid rps":          {withEnv("RATE_LIMIT_RPS", "-1")},
		"invalid body limit":   {withEnv("MAX_BODY_BYTES", "1MB")},
		"invalid port":         {withEnv("PORT", "not-a-port")},
		"invalid mongo uri":    {withEnv("MONGO_URI", "postgres://nope")},
		"unreachable database": {withEnv("MONGO_URI", "mongodb://127.0.0.1:1/?serverSelectionTimeoutMS=500")},
	}
	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			s := launch(t, raceBinary, opts...)
			select {
			case <-s.exited:
			case <-time.After(20 * time.Second):
				_ = s.cmd.Process.Kill()
				t.Fatal("server started with invalid configuration")
			}
			var exitErr *exec.ExitError
			if !errors.As(s.exitErr, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("exit = %v, want status 1", s.exitErr)
			}
			if !strings.Contains(s.logs.String(), "server stopped") {
				t.Errorf("no error logged:\n%s", s.logs)
			}
		})
	}
}

func TestStartupFailsWhenPortIsTaken(t *testing.T) {
	if image != "" {
		t.Skip("the container port is always free")
	}
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	s := launch(t, raceBinary, withEnv("PORT", fmt.Sprint(l.Addr().(*net.TCPAddr).Port)))
	select {
	case <-s.exited:
	case <-time.After(20 * time.Second):
		_ = s.cmd.Process.Kill()
		t.Fatal("server kept running with its port taken")
	}
	if s.exitErr == nil || !strings.Contains(s.logs.String(), "address already in use") {
		t.Errorf("exit = %v logs:\n%s", s.exitErr, s.logs)
	}
}
