package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestServeDeliversToEveryConnectionOfTheUser(t *testing.T) {
	h := NewHub(fastOptions())
	srv := newTestServer(t, h)
	tabs := connect(t, h, srv, "bob", 3)
	other := connect(t, h, srv, "carol", 1)[0]

	payloads := []string{`{"content":"one"}`, `{"content":"日本語 🚀"}`, `{"content":"<b>html</b>"}`}
	for _, p := range payloads {
		if n := h.Broadcast([]string{"bob"}, []byte(p)); n != len(tabs) {
			t.Fatalf("delivered = %d", n)
		}
	}
	for i, tab := range tabs {
		for _, want := range payloads {
			if got := readText(t, tab); got != want {
				t.Errorf("tab %d got %q, want %q", i, got, want)
			}
		}
	}
	h.Broadcast([]string{"carol"}, []byte(`{"to":"carol"}`))
	if got := readText(t, other); got != `{"to":"carol"}` {
		t.Errorf("carol got %q; she received bob's messages", got)
	}
}

func TestServeSendsEveryMessageAsItsOwnFrameInOrder(t *testing.T) {
	h := NewHub(fastOptions())
	srv := newTestServer(t, h)
	conn := connect(t, h, srv, "bob", 1)[0]

	const burst = 300
	for i := range burst {
		h.Broadcast([]string{"bob"}, fmt.Appendf(nil, `{"seq":%d}`, i))
	}
	for i := range burst {
		var msg struct{ Seq int }
		got := readText(t, conn)
		if err := json.Unmarshal([]byte(got), &msg); err != nil {
			t.Fatalf("frame %d is not one JSON document: %q", i, got)
		}
		if msg.Seq != i {
			t.Fatalf("frame %d has seq %d", i, msg.Seq)
		}
	}
}

func TestServeUnregistersOnClientClose(t *testing.T) {
	h := NewHub(fastOptions())
	srv := newTestServer(t, h)
	tabs := connect(t, h, srv, "bob", 3)

	_ = tabs[0].WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	_ = tabs[1].UnderlyingConn().Close()
	waitFor(t, "two tabs unregistered", func() bool { return connections(h, "bob") == 1 })

	h.Broadcast([]string{"bob"}, []byte(`{"still":"here"}`))
	if got := readText(t, tabs[2]); got != `{"still":"here"}` {
		t.Errorf("remaining tab got %q", got)
	}
}

func TestServeIgnoresInboundMessages(t *testing.T) {
	h := NewHub(fastOptions())
	srv := newTestServer(t, h)
	bob := connect(t, h, srv, "bob", 1)[0]
	carol := connect(t, h, srv, "carol", 1)[0]

	for range 20 {
		_ = bob.WriteMessage(websocket.TextMessage, []byte(`{"to":"carol","spoof":true}`))
	}
	time.Sleep(100 * time.Millisecond)
	h.Broadcast([]string{"bob", "carol"}, []byte(`{"marker":1}`))
	for name, conn := range map[string]*websocket.Conn{"bob": bob, "carol": carol} {
		if got := readText(t, conn); got != `{"marker":1}` {
			t.Errorf("%s first message %q: inbound data was relayed", name, got)
		}
	}
}

func TestServeClosesOnOversizedInboundMessage(t *testing.T) {
	opts := fastOptions()
	opts.ReadLimit = 64
	h := NewHub(opts)
	srv := newTestServer(t, h)
	conn := connect(t, h, srv, "bob", 1)[0]

	_ = conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 65)))
	expectClose(t, conn, websocket.CloseMessageTooBig)
	waitFor(t, "unregistered", func() bool { return connections(h, "bob") == 0 })
}

func TestServeSendsPingsAndKeepsResponsiveClients(t *testing.T) {
	opts := fastOptions()
	opts.PingPeriod = 30 * time.Millisecond
	opts.PongWait = 150 * time.Millisecond
	h := NewHub(opts)
	srv := newTestServer(t, h)
	conn := connect(t, h, srv, "bob", 1)[0]

	var mu sync.Mutex
	pings := 0
	conn.SetPingHandler(func(data string) error {
		mu.Lock()
		pings++
		mu.Unlock()
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
	})
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _, _ = conn.ReadMessage()

	if connections(h, "bob") != 1 {
		t.Fatal("responsive client was dropped")
	}
	mu.Lock()
	defer mu.Unlock()
	if pings < 5 {
		t.Errorf("received %d pings in 500ms", pings)
	}
}

func TestServeDropsClientsThatStopAnsweringPings(t *testing.T) {
	opts := fastOptions()
	opts.PingPeriod = 20 * time.Millisecond
	opts.PongWait = 100 * time.Millisecond
	h := NewHub(opts)
	srv := newTestServer(t, h)
	connect(t, h, srv, "bob", 1)
	waitFor(t, "unresponsive client dropped", func() bool { return connections(h, "bob") == 0 })
}

func TestServeEvictsSlowConsumerWithoutAffectingOthers(t *testing.T) {
	opts := fastOptions()
	opts.MaxQueuedBytes = 4 << 20
	opts.WriteWait = 200 * time.Millisecond
	h := NewHub(opts)
	srv := newTestServer(t, h)
	slow := connect(t, h, srv, "slow", 1)[0]
	fast := connect(t, h, srv, "fast", 1)[0]

	received := make(chan int, 1)
	go func() {
		count := 0
		for {
			_ = fast.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, _, err := fast.ReadMessage(); err != nil {
				received <- count
				return
			}
			count++
		}
	}()

	payload := []byte(`{"blob":"` + strings.Repeat("x", 256<<10) + `"}`)
	const messages = 200
	for range messages {
		h.Broadcast([]string{"slow", "fast"}, payload)
		time.Sleep(2 * time.Millisecond)
	}
	waitFor(t, "slow consumer evicted", func() bool { return connections(h, "slow") == 0 })
	_ = slow.Close()
	h.Broadcast([]string{"fast"}, []byte(`{"last":true}`))
	time.Sleep(200 * time.Millisecond)
	_ = fast.Close()
	if got := <-received; got != messages+1 {
		t.Errorf("fast client received %d, want %d", got, messages+1)
	}
}

func TestServeShutdownSendsGoingAway(t *testing.T) {
	h := NewHub(fastOptions())
	srv := newTestServer(t, h)
	conns := append(connect(t, h, srv, "alice", 2), connect(t, h, srv, "bob", 1)...)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for _, conn := range conns {
		expectClose(t, conn, websocket.CloseGoingAway)
	}
	late := dial(t, srv, "carol")
	expectClose(t, late, websocket.CloseGoingAway)
	if connections(h, "carol") != 0 {
		t.Error("registered after shutdown")
	}
}

func TestServeConcurrentChurnAndBroadcasts(t *testing.T) {
	h := NewHub(fastOptions())
	srv := newTestServer(t, h)
	stable := connect(t, h, srv, "bob", 4)

	stop := make(chan struct{})
	var churn sync.WaitGroup
	for range 8 {
		churn.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = dial(t, srv, "bob").Close()
			}
		})
	}

	const senders, perSender = 8, 50
	var send sync.WaitGroup
	for s := range senders {
		send.Go(func() {
			for i := range perSender {
				h.Broadcast([]string{"bob"}, fmt.Appendf(nil, `{"s":%d,"i":%d}`, s, i))
			}
		})
	}
	send.Wait()
	close(stop)
	churn.Wait()

	for idx, conn := range stable {
		next := make([]int, senders)
		for range senders * perSender {
			var msg struct{ S, I int }
			if err := json.Unmarshal([]byte(readText(t, conn)), &msg); err != nil {
				t.Fatal(err)
			}
			if msg.I != next[msg.S] {
				t.Fatalf("conn %d: sender %d message %d, expected %d", idx, msg.S, msg.I, next[msg.S])
			}
			next[msg.S]++
		}
	}
	waitFor(t, "churned connections unregistered", func() bool { return connections(h, "bob") == len(stable) })
}
