//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type sequenced struct {
	Sender string
	Seq    int
}

func parseSequenced(t *testing.T, m chatMessage) sequenced {
	t.Helper()
	var seq int
	if _, err := fmt.Sscanf(m.Content, "seq=%d", &seq); err != nil {
		t.Fatalf("unexpected content %q", m.Content)
	}
	return sequenced{m.Sender, seq}
}

// assertPerSenderOrder checks every message arrived exactly once and each
// sender's messages arrived in the order they were acknowledged.
func assertPerSenderOrder(t *testing.T, tb *tab, senders []string, perSender int) {
	t.Helper()
	next := map[string]int{}
	for _, m := range tb.messages(t, len(senders)*perSender, 30*time.Second) {
		s := parseSequenced(t, m)
		if s.Seq != next[s.Sender] {
			t.Fatalf("%s: %s's message %d arrived, expected %d", tb.user, s.Sender, s.Seq, next[s.Sender])
		}
		next[s.Sender]++
	}
	tb.expectNothing(t, 0)
}

func sendConcurrently(t *testing.T, s *server, senders, group []string, perSender int) {
	t.Helper()
	var wg sync.WaitGroup
	var failures atomic.Int64
	for _, sender := range senders {
		wg.Go(func() {
			for i := range perSender {
				if s.send(t, sender, group, fmt.Sprintf("seq=%d", i)).status != http.StatusAccepted {
					failures.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if n := failures.Load(); n > 0 {
		t.Fatalf("%d sends failed", n)
	}
}

func TestConcurrentSendersInAGroup(t *testing.T) {
	s := startServer(t)
	members, senders := names("member", 30), names("sender", 15)
	tabs := s.openUsers(t, members)
	group := append(append([]string{}, members...), senders...)

	const perSender = 30
	sendConcurrently(t, s, senders, group, perSender)
	for _, tb := range tabs {
		assertPerSenderOrder(t, tb, senders, perSender)
	}
	s.awaitPersisted(t, members[0], group, len(senders)*perSender, 20*time.Second)
}

func TestConcurrentConversationsStayIsolated(t *testing.T) {
	s := startServer(t)
	const pairs, perPair = 100, 10
	receivers := s.openUsers(t, names("receiver", pairs))
	var wg sync.WaitGroup
	for i := range pairs {
		wg.Go(func() {
			sender := fmt.Sprint("sender-", i)
			for j := range perPair {
				if res := s.send(t, sender, []string{sender, receivers[i].user}, fmt.Sprintf("seq=%d", j)); res.status != http.StatusAccepted {
					t.Errorf("send: %d", res.status)
				}
			}
		})
	}
	wg.Wait()
	for i, tb := range receivers {
		for j, m := range tb.messages(t, perPair, 20*time.Second) {
			if m.Sender != fmt.Sprint("sender-", i) || m.Content != fmt.Sprintf("seq=%d", j) {
				t.Fatalf("%s got %+v: conversations crossed", tb.user, m)
			}
		}
		tb.expectNothing(t, 0)
	}
}

func TestTabChurnDuringBroadcasts(t *testing.T) {
	s := startServer(t)
	members := names("member", 10)
	stable := s.openUsers(t, members)

	stop := make(chan struct{})
	var churn sync.WaitGroup
	var churned atomic.Int64
	for w := range 16 {
		churn.Go(func() {
			token := s.userToken(t, members[w%len(members)])
			for {
				select {
				case <-stop:
					return
				default:
				}
				conn, _, err := s.dial(token, nil)
				if err != nil {
					continue
				}
				churned.Add(1)
				switch w % 3 {
				case 0:
					_ = conn.UnderlyingConn().Close()
				case 1:
					_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
					_ = conn.Close()
				default:
					_ = conn.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
					_, _, _ = conn.ReadMessage()
					_ = conn.Close()
				}
			}
		})
	}

	senders := names("sender", 8)
	const perSender = 40
	sendConcurrently(t, s, senders, append(append([]string{}, members...), senders...), perSender)
	close(stop)
	churn.Wait()
	for _, tb := range stable {
		assertPerSenderOrder(t, tb, senders, perSender)
	}
	t.Logf("%d tabs churned during %d messages", churned.Load(), len(senders)*perSender)
}

func TestSlowConsumerDoesNotStallOthers(t *testing.T) {
	s := startServer(t, withEnv("MAX_BODY_BYTES", "262144"))
	conn, _, err := s.dial(s.userToken(t, "slow"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fast := s.openUsers(t, names("fast", 5))
	s.awaitConnections(t, map[string]int{"slow": 1})
	group := append([]string{"sender", "slow"}, names("fast", 5)...)

	blob := strings.Repeat("x", 200<<10)
	const messages = 300
	for i := range messages {
		if res := s.send(t, "sender", group, fmt.Sprintf("seq=%d %s", i, blob)); res.status != http.StatusAccepted {
			t.Fatalf("send %d: %d", i, res.status)
		}
	}
	for _, tb := range fast {
		for i, m := range tb.messages(t, messages, 60*time.Second) {
			if !strings.HasPrefix(m.Content, fmt.Sprintf("seq=%d ", i)) {
				t.Fatalf("%s message %d out of order", tb.user, i)
			}
		}
	}

	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	received := 0
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
		received++
	}
	if received >= messages {
		t.Fatalf("slow consumer received all %d messages; it was never evicted", received)
	}
	t.Logf("slow consumer evicted after %d of %d messages", received, messages)
}

func TestShutdownUnderLoadPersistsAcknowledgedMessages(t *testing.T) {
	db := "e2e_load_" + randomSecret()[:8]
	s := startServer(t, withDatabase(db))
	tabs := s.openUsers(t, names("reader", 20))
	channel := append([]string{"writer"}, names("reader", 20)...)

	var acknowledged, transportErrors atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	token := s.userToken(t, "writer")
	for range 16 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				req, _ := http.NewRequest(http.MethodPost, s.baseURL+"/api/messages", strings.NewReader(string(sendBody(channel, "load"))))
				req.Header.Set("Authorization", "Bearer "+token)
				resp, err := httpClient.Do(req)
				if err != nil {
					transportErrors.Add(1)
					continue
				}
				if resp.StatusCode == http.StatusAccepted {
					acknowledged.Add(1)
				}
				_ = resp.Body.Close()
			}
		})
	}

	time.Sleep(time.Second)
	if err := s.stop(30 * time.Second); err != nil {
		t.Errorf("exit under load: %v", err)
	}
	close(stop)
	wg.Wait()
	for _, tb := range tabs {
		if err := tb.waitClosed(t, 5*time.Second); !websocket.IsCloseError(err, websocket.CloseGoingAway) {
			t.Errorf("%s close = %v, want 1001", tb.user, err)
		}
	}

	restarted := startServer(t, withDatabase(db))
	acked, errs := int(acknowledged.Load()), int(transportErrors.Load())
	var stored int
	for page := 0; ; page++ {
		batch := restarted.history(t, "writer", channel, page, 100)
		stored += len(batch)
		if len(batch) < 100 {
			break
		}
	}
	if stored < acked || stored > acked+errs {
		t.Fatalf("stored %d messages; %d were acknowledged and %d requests failed in transit", stored, acked, errs)
	}
	t.Logf("%d acknowledged, %d stored across a SIGTERM under load", acked, stored)
}

func TestBackpressureWhenDatabaseStalls(t *testing.T) {
	s := startServer(t)
	bob := s.open(t, "bob")
	resume := pauseMongo(t)

	var accepted, unavailable atomic.Int64
	var slowest atomic.Int64
	var wg sync.WaitGroup
	token := s.userToken(t, "alice")
	for range 32 {
		wg.Go(func() {
			for range 60 {
				start := time.Now()
				res := s.request(t, http.MethodPost, "/api/messages", token, sendBody([]string{"alice", "bob"}, "while the database is stalled"))
				if d := int64(time.Since(start)); d > slowest.Load() {
					slowest.Store(d)
				}
				switch res.status {
				case http.StatusAccepted:
					accepted.Add(1)
				case http.StatusServiceUnavailable:
					unavailable.Add(1)
					if res.header.Get("Retry-After") == "" {
						t.Error("503 without Retry-After")
					}
				default:
					t.Errorf("unexpected status %d", res.status)
				}
			}
		})
	}
	wg.Wait()
	resume()

	if unavailable.Load() == 0 {
		t.Fatalf("no backpressure: %d accepted with a stalled database", accepted.Load())
	}
	if d := time.Duration(slowest.Load()); d > 2*time.Second {
		t.Errorf("a request blocked for %s while the database was stalled", d)
	}
	bob.messages(t, int(accepted.Load()), 30*time.Second)
	bob.expectNothing(t, 200*time.Millisecond)
	s.awaitPersisted(t, "alice", []string{"alice", "bob"}, int(accepted.Load()), 60*time.Second)
	t.Logf("%d accepted and delivered, %d rejected with 503", accepted.Load(), unavailable.Load())
}

func TestConnectWhileMessagesFlow(t *testing.T) {
	s := startServer(t)
	users := names("joiner", 100)
	group := append([]string{"chatter"}, users...)

	stop := make(chan struct{})
	var background sync.WaitGroup
	background.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			s.send(t, "chatter", group, "background")
		}
	})
	tabs := s.openUsers(t, users)
	close(stop)
	background.Wait()

	s.mustSend(t, "chatter", group, "final")
	for _, tb := range tabs {
		for {
			if tb.next(t, 30*time.Second).Content == "final" {
				break
			}
		}
	}
}
