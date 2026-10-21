package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"chat-microservice/pkg/models"
)

type fakeStore struct {
	mu       sync.Mutex
	saved    []*models.Message
	batches  []int
	attempts int
	failures int
	gate     chan struct{}
	history  []*models.Message
	err      error
	query    struct {
		participants []string
		page, size   int
	}
}

func (f *fakeStore) SaveBatch(ctx context.Context, msgs []*models.Message) error {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.failures > 0 {
		f.failures--
		return errors.New("transient")
	}
	f.saved = append(f.saved, msgs...)
	f.batches = append(f.batches, len(msgs))
	return nil
}

func (f *fakeStore) History(_ context.Context, participants []string, page, size int) ([]*models.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.query.participants, f.query.page, f.query.size = participants, page, size
	return f.history, f.err
}

func (f *fakeStore) savedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.saved)
}

type broadcast struct {
	recipients []string
	payload    []byte
}

type fakeHub struct {
	mu    sync.Mutex
	calls []broadcast
}

func (f *fakeHub) Broadcast(recipients []string, payload []byte) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, broadcast{slices.Clone(recipients), payload})
	return len(recipients)
}

func (f *fakeHub) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func testOptions() Options {
	return Options{Workers: 2, QueueSize: 16, MaxBatch: 8, MaxRetries: 3, RetryBackoff: time.Millisecond, SaveTimeout: time.Second}
}

func newTestService(t *testing.T, store *fakeStore, opts Options) (*ChatService, *fakeHub) {
	t.Helper()
	hub := &fakeHub{}
	svc := NewChatService(store, hub, opts)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Close(ctx)
	})
	return svc, hub
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestSendBroadcastsToEveryoneButSenderAndPersists(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(t, store, testOptions())

	before := time.Now().UTC().Add(-time.Millisecond)
	msg, err := svc.Send("alice", []string{"carol", " bob", "alice", "bob"}, "hello")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if msg.ID.IsZero() {
		t.Error("message has no ID")
	}
	if !slices.Equal(msg.Participants, []string{"alice", "bob", "carol"}) {
		t.Errorf("participants = %q", msg.Participants)
	}
	if msg.Sender != "alice" || msg.Content != "hello" {
		t.Errorf("message = %+v", msg)
	}
	if msg.CreatedAt.Location() != time.UTC || msg.CreatedAt.Nanosecond()%int(time.Millisecond) != 0 || msg.CreatedAt.Before(before) {
		t.Errorf("CreatedAt = %v, want UTC truncated to ms", msg.CreatedAt)
	}

	if hub.count() != 1 {
		t.Fatalf("broadcasts = %d", hub.count())
	}
	call := hub.calls[0]
	if !slices.Equal(call.recipients, []string{"bob", "carol"}) {
		t.Errorf("recipients = %q, want everyone but the sender exactly once", call.recipients)
	}
	var wire models.Message
	if err := json.Unmarshal(call.payload, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.ID != msg.ID || !wire.CreatedAt.Equal(msg.CreatedAt) || !slices.Equal(wire.Participants, msg.Participants) {
		t.Errorf("payload %s does not describe %+v", call.payload, msg)
	}

	waitFor(t, "message persisted", func() bool { return store.savedCount() == 1 })
	if store.saved[0] != msg {
		t.Error("persisted a different message")
	}
}

func TestSendToSelfOnlyChannel(t *testing.T) {
	svc, hub := newTestService(t, &fakeStore{}, testOptions())
	if _, err := svc.Send("alice", []string{"alice", "alice"}, "note to self"); err != nil {
		t.Fatal(err)
	}
	if len(hub.calls[0].recipients) != 0 {
		t.Errorf("recipients = %q", hub.calls[0].recipients)
	}
}

func TestSendValidation(t *testing.T) {
	tooMany := make([]string, models.MaxParticipants+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprint("u", i)
	}
	tests := []struct {
		name         string
		sender       string
		participants []string
		content      string
		want         error
	}{
		{"no participants", "alice", nil, "hi", models.ErrNoParticipants},
		{"empty participant", "alice", []string{"alice", ""}, "hi", models.ErrEmptyParticipant},
		{"too many", "u0", tooMany, "hi", models.ErrTooManyParticipants},
		{"sender not participant", "mallory", []string{"alice", "bob"}, "hi", ErrNotParticipant},
		{"sender differs by case", "Alice", []string{"alice", "bob"}, "hi", ErrNotParticipant},
		{"empty content", "alice", []string{"alice", "bob"}, "", ErrEmptyContent},
		{"whitespace content", "alice", []string{"alice", "bob"}, " \n\t ", ErrEmptyContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{}
			svc, hub := newTestService(t, store, testOptions())
			if _, err := svc.Send(tt.sender, tt.participants, tt.content); !errors.Is(err, tt.want) {
				t.Fatalf("Send error = %v, want %v", err, tt.want)
			}
			time.Sleep(10 * time.Millisecond)
			if hub.count() != 0 || store.savedCount() != 0 {
				t.Errorf("rejected message was broadcast (%d) or saved (%d)", hub.count(), store.savedCount())
			}
		})
	}
}

func TestSendPreservesContentVerbatim(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(t, store, testOptions())
	content := "  <script>alert(1)</script> \u0000 日本語 🚀 \"quoted\"\n"
	if _, err := svc.Send("alice", []string{"alice", "bob"}, content); err != nil {
		t.Fatal(err)
	}
	var wire models.Message
	_ = json.Unmarshal(hub.calls[0].payload, &wire)
	if wire.Content != content {
		t.Errorf("content altered: %q", wire.Content)
	}
}

func TestSendFailsFastWhenPersistenceIsFull(t *testing.T) {
	store := &fakeStore{gate: make(chan struct{})}
	opts := testOptions()
	opts.Workers, opts.QueueSize = 1, 2
	svc, hub := newTestService(t, store, opts)
	defer close(store.gate)

	accepted := 0
	var lastErr error
	start := time.Now()
	for range 10 {
		if _, err := svc.Send("alice", []string{"alice", "bob"}, "hi"); err != nil {
			lastErr = err
			continue
		}
		accepted++
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Send blocked for %s with a full queue", elapsed)
	}
	if !errors.Is(lastErr, ErrOverloaded) {
		t.Fatalf("error = %v, want ErrOverloaded", lastErr)
	}
	if accepted < opts.QueueSize || accepted > opts.QueueSize+opts.MaxBatch {
		t.Errorf("accepted = %d, want the queue size plus at most one in-flight batch", accepted)
	}
	if hub.count() != accepted {
		t.Errorf("broadcasts = %d, accepted = %d: rejected messages must not be delivered", hub.count(), accepted)
	}
}

func TestQueuedMessagesArePersistedInBatches(t *testing.T) {
	store := &fakeStore{gate: make(chan struct{})}
	opts := testOptions()
	opts.Workers, opts.QueueSize, opts.MaxBatch = 1, 64, 10
	svc, _ := newTestService(t, store, opts)

	const queued = 41
	for i := range queued {
		if _, err := svc.Send("alice", []string{"alice", "bob"}, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	close(store.gate)
	waitFor(t, "all messages persisted", func() bool { return store.savedCount() == queued })

	store.mu.Lock()
	defer store.mu.Unlock()
	for _, n := range store.batches {
		if n > opts.MaxBatch {
			t.Errorf("batch of %d exceeds MaxBatch %d", n, opts.MaxBatch)
		}
	}
	if len(store.batches) > 6 {
		t.Errorf("%d round trips for %d queued messages: %v", len(store.batches), queued, store.batches)
	}
	for i, m := range store.saved {
		if m.Content != fmt.Sprint(i) {
			t.Fatalf("persisted out of order at %d: %q", i, m.Content)
		}
	}
}

func TestSaveRetriesTransientFailures(t *testing.T) {
	store := &fakeStore{failures: 2}
	svc, _ := newTestService(t, store, testOptions())
	if _, err := svc.Send("alice", []string{"alice", "bob"}, "hi"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "message persisted after retries", func() bool { return store.savedCount() == 1 })
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.attempts != 3 {
		t.Errorf("attempts = %d, want 3", store.attempts)
	}
}

func TestSaveGivesUpAfterMaxRetries(t *testing.T) {
	store := &fakeStore{failures: 100}
	svc, _ := newTestService(t, store, testOptions())
	if _, err := svc.Send("alice", []string{"alice", "bob"}, "hi"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if store.attempts != 3 || store.savedCount() != 0 {
		t.Errorf("attempts = %d saved = %d, want 3 and 0", store.attempts, store.savedCount())
	}
}

func TestCloseDrainsQueuedMessages(t *testing.T) {
	store := &fakeStore{gate: make(chan struct{})}
	opts := testOptions()
	opts.Workers, opts.QueueSize = 2, 64
	svc, _ := newTestService(t, store, opts)

	const queued = 40
	for i := range queued {
		if _, err := svc.Send("alice", []string{"alice", "bob"}, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(store.gate)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := store.savedCount(); got != queued {
		t.Fatalf("saved %d of %d acknowledged messages before shutdown", got, queued)
	}
	if _, err := svc.Send("alice", []string{"alice", "bob"}, "late"); !errors.Is(err, ErrClosed) {
		t.Errorf("Send after Close = %v, want ErrClosed", err)
	}
	if err := svc.Close(ctx); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestCloseHonoursContext(t *testing.T) {
	store := &fakeStore{gate: make(chan struct{})}
	opts := testOptions()
	opts.SaveTimeout = time.Hour
	svc, _ := newTestService(t, store, opts)
	defer close(store.gate)
	_, _ = svc.Send("alice", []string{"alice", "bob"}, "stuck")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := svc.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want deadline exceeded", err)
	}
}

func TestConcurrentSendAndClose(t *testing.T) {
	store := &fakeStore{}
	opts := testOptions()
	opts.QueueSize = 4096
	svc, hub := newTestService(t, store, opts)

	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for w := range 16 {
		wg.Go(func() {
			for i := range 100 {
				_, err := svc.Send(fmt.Sprint("u", w), []string{fmt.Sprint("u", w), "hub"}, fmt.Sprint(i))
				switch {
				case err == nil:
					mu.Lock()
					accepted++
					mu.Unlock()
				case errors.Is(err, ErrClosed), errors.Is(err, ErrOverloaded):
				default:
					t.Errorf("unexpected error %v", err)
				}
			}
		})
	}
	time.Sleep(time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Close(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if store.savedCount() != accepted || hub.count() != accepted {
		t.Errorf("accepted %d, saved %d, broadcast %d", accepted, store.savedCount(), hub.count())
	}
}

func TestHistory(t *testing.T) {
	stored := []*models.Message{{Content: "a"}, {Content: "b"}}
	store := &fakeStore{history: stored}
	svc, _ := newTestService(t, store, testOptions())

	got, err := svc.History(context.Background(), "bob", []string{" carol", "bob", "alice", "bob"}, 2, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != stored[0] {
		t.Errorf("History = %v", got)
	}
	if !slices.Equal(store.query.participants, []string{"alice", "bob", "carol"}) || store.query.page != 2 || store.query.size != 25 {
		t.Errorf("query = %+v", store.query)
	}
}

func TestHistoryErrors(t *testing.T) {
	store := &fakeStore{err: errors.New("db down")}
	svc, _ := newTestService(t, store, testOptions())
	if _, err := svc.History(context.Background(), "mallory", []string{"alice", "bob"}, 0, 10); !errors.Is(err, ErrNotParticipant) {
		t.Errorf("outsider: %v", err)
	}
	if _, err := svc.History(context.Background(), "alice", []string{"alice", ""}, 0, 10); !errors.Is(err, models.ErrEmptyParticipant) {
		t.Errorf("empty participant: %v", err)
	}
	if _, err := svc.History(context.Background(), "alice", []string{"alice", "bob"}, 0, 10); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Errorf("store error not propagated: %v", err)
	}
}

func BenchmarkSend(b *testing.B) {
	store := &fakeStore{}
	opts := testOptions()
	opts.QueueSize = 1 << 20
	svc := NewChatService(store, &fakeHub{}, opts)
	defer svc.Close(context.Background())
	group := []string{"alice", "bob", "carol", "dave"}
	b.ReportAllocs()
	for b.Loop() {
		_, _ = svc.Send("alice", group, "hello there")
	}
}
