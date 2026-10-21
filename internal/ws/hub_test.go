package ws

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestBroadcastWithoutConnections(t *testing.T) {
	h := NewHub(DefaultOptions())
	if n := h.Broadcast([]string{"nobody"}, []byte(`{}`)); n != 0 {
		t.Fatalf("Broadcast = %d", n)
	}
	if n := h.Broadcast(nil, []byte(`{}`)); n != 0 {
		t.Fatalf("Broadcast(nil) = %d", n)
	}
}

func TestRegisterUnregisterBookkeeping(t *testing.T) {
	h := NewHub(DefaultOptions())
	a, b, c := fakeClient("alice", 1), fakeClient("alice", 1), fakeClient("bob", 1)
	for _, cl := range []*Client{a, b, c} {
		if !h.register(cl) {
			t.Fatal("register rejected on open hub")
		}
	}
	defer h.clients.Add(-3)

	want := map[string]int{"alice": 2, "bob": 1, "carol": 0}
	if got := h.ConnectionCounts([]string{"alice", "bob", "carol"}); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("counts = %v, want %v", got, want)
	}
	h.unregister(a)
	h.unregister(a)
	h.unregister(fakeClient("ghost", 1))
	if connections(h, "alice") != 1 {
		t.Fatalf("alice connections = %d", connections(h, "alice"))
	}
	h.unregister(b)
	if _, ok := h.users["alice"]; ok {
		t.Fatal("empty user entry not removed")
	}
}

func TestConnectionCountsEdgeCases(t *testing.T) {
	h := NewHub(DefaultOptions())
	if got := h.ConnectionCounts(nil); len(got) != 0 {
		t.Errorf("counts(nil) = %v", got)
	}
	if got := h.ConnectionCounts([]string{"a", "a", ""}); len(got) != 2 || got["a"] != 0 || got[""] != 0 {
		t.Errorf("counts = %v", got)
	}
}

func TestBroadcastReachesEveryConnectionOfEveryRecipient(t *testing.T) {
	h := NewHub(DefaultOptions())
	alice := []*Client{fakeClient("alice", 64), fakeClient("alice", 64)}
	bob := fakeClient("bob", 64)
	carol := fakeClient("carol", 64)
	for _, c := range append(alice, bob, carol) {
		h.register(c)
	}
	defer h.clients.Add(-4)

	if n := h.Broadcast([]string{"alice", "bob", "dave"}, []byte(`{"m":1}`)); n != 3 {
		t.Fatalf("delivered = %d, want 3", n)
	}
	for i, c := range append(alice, bob) {
		if queued(c) != 1 {
			t.Errorf("recipient %d queued %d", i, queued(c))
		}
	}
	if queued(carol) != 0 {
		t.Error("non-recipient received the message")
	}
}

func TestBroadcastDisconnectsSlowClients(t *testing.T) {
	h := NewHub(DefaultOptions())
	slow, fast := fakeClient("bob", 1), fakeClient("carol", 2)
	h.register(slow)
	h.register(fast)
	defer h.clients.Add(-2)

	if n := h.Broadcast([]string{"bob", "carol"}, []byte(`1`)); n != 2 {
		t.Fatalf("first = %d", n)
	}
	if n := h.Broadcast([]string{"bob", "carol"}, []byte(`2`)); n != 1 {
		t.Fatalf("second = %d", n)
	}
	if !isClosed(slow) || string(slow.closeFrame) != string(closeSlow) {
		t.Error("slow client not closed with 1008")
	}
	if isClosed(fast) || queued(fast) != 2 {
		t.Errorf("fast client closed=%v queued=%d", isClosed(fast), queued(fast))
	}
	if n := h.Broadcast([]string{"bob"}, []byte(`3`)); n != 0 {
		t.Errorf("closed client accepted a message")
	}
}

func TestQueueIsBoundedByBytesNotMessageCount(t *testing.T) {
	c := fakeClient("bob", 64<<10)
	payload := []byte(`{"id":"0123456789abcdef01234567","content":"hi"}`)
	burst := (64 << 10) / len(payload)
	for i := range burst {
		if !c.enqueue(payload) {
			t.Fatalf("message %d of a %d burst rejected", i, burst)
		}
	}
	if c.enqueue(payload) {
		t.Fatal("message beyond the byte budget accepted")
	}
}

func TestQueueAcceptsOversizedMessageWhenEmpty(t *testing.T) {
	c := fakeClient("bob", 10)
	if !c.enqueue(make([]byte, 100)) {
		t.Fatal("oversized message rejected by empty queue")
	}
	if c.enqueue([]byte{1}) {
		t.Fatal("accepted on top of over-budget queue")
	}
	drain(c)
	if !c.enqueue(make([]byte, 100)) {
		t.Fatal("budget not released after drain")
	}
}

func TestQueueBuffersNeverAlias(t *testing.T) {
	c := fakeClient("bob", 1<<20)
	c.enqueue([]byte("x"))
	drain(c)
	takeQueue(c)

	c.enqueue([]byte("y"))
	batch := takeQueue(c)
	c.enqueue([]byte("z"))
	if len(batch) != 1 || string(batch[0].payload) != "y" {
		t.Fatalf("in-flight batch overwritten: %q", batch[0].payload)
	}
}

func TestReleaseTracksQueuedBytes(t *testing.T) {
	c := fakeClient("bob", 100)
	for range 4 {
		c.enqueue(make([]byte, 25))
	}
	batch := takeQueue(c)
	if c.enqueue([]byte{1}) && c.enqueue([]byte{1}) {
		t.Fatal("bytes released before the batch was written")
	}
	c.release(batch)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queuedBytes != 1 {
		t.Fatalf("queuedBytes = %d, want 1", c.queuedBytes)
	}
}

func TestWriterIsClaimedOnce(t *testing.T) {
	c := newClient(nil, "bob", DefaultOptions(), func() {})
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.claimWriter() || c.claimWriter() {
		t.Fatal("writer claim is not exclusive")
	}
}

func TestClosedClientRejectsMessagesAndPings(t *testing.T) {
	c := fakeClient("bob", 1<<20)
	c.close(closeSlow)
	c.close(closeShutdown)
	if string(c.closeFrame) != string(closeSlow) {
		t.Error("second close overrode the first frame")
	}
	if c.enqueue([]byte(`{}`)) {
		t.Fatal("closed client accepted a message")
	}
	c.ping()
	if queued(c) != 0 {
		t.Fatal("closed client queued a ping")
	}
}

func TestShutdownClosesClientsAndRejectsNewOnes(t *testing.T) {
	h := NewHub(DefaultOptions())
	clients := []*Client{fakeClient("alice", 1), fakeClient("bob", 1)}
	for _, c := range clients {
		h.register(c)
		h.clients.Done()
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range clients {
		if string(c.closeFrame) != string(closeShutdown) {
			t.Errorf("%s not closed with 1001", c.userID)
		}
	}
	if h.register(fakeClient("carol", 1)) {
		t.Error("register accepted after shutdown")
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Errorf("second Shutdown: %v", err)
	}
}

func TestShutdownHonoursContext(t *testing.T) {
	h := NewHub(DefaultOptions())
	h.register(fakeClient("stuck", 1))
	defer h.clients.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := h.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown = %v", err)
	}
}

func TestConcurrentRegisterBroadcastUnregister(t *testing.T) {
	h := NewHub(DefaultOptions())
	var wg sync.WaitGroup
	for w := range 16 {
		wg.Go(func() {
			user := fmt.Sprint("u", w%4)
			for range 300 {
				c := fakeClient(user, 1<<10)
				h.register(c)
				h.Broadcast([]string{"u0", "u1", "u2", "u3"}, []byte(`{}`))
				_ = h.ConnectionCounts([]string{user})
				h.unregister(c)
				h.clients.Done()
			}
		})
	}
	wg.Wait()
	if len(h.users) != 0 {
		t.Fatalf("users leaked: %d", len(h.users))
	}
}

func BenchmarkBroadcast(b *testing.B) {
	for _, recipients := range []int{1, 10, 100, 1000} {
		b.Run(fmt.Sprintf("recipients=%d", recipients), func(b *testing.B) {
			h := NewHub(DefaultOptions())
			users := make([]string, recipients)
			clients := make([]*Client, recipients)
			for i := range users {
				users[i] = fmt.Sprint("user-", i)
				clients[i] = fakeClient(users[i], 1<<20)
				h.register(clients[i])
			}
			payload := []byte(`{"id":"6ad83f903e12f4ca9fcef56e","sender":"alice","content":"hello"}`)
			b.ReportAllocs()
			for b.Loop() {
				h.Broadcast(users, payload)
				for _, c := range clients {
					drain(c)
				}
			}
		})
	}
}

func BenchmarkBroadcastParallelConversations(b *testing.B) {
	h := NewHub(DefaultOptions())
	stop := make(chan struct{})
	defer close(stop)
	const users = 1024
	names := make([]string, users)
	for i := range users {
		names[i] = fmt.Sprint("user-", i)
		c := fakeClient(names[i], 1<<20)
		h.register(c)
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
					drain(c)
					runtime.Gosched()
				}
			}
		}()
	}
	payload := []byte(`{"content":"hello"}`)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			h.Broadcast(names[i%users:i%users+1], payload)
			i++
		}
	})
}
