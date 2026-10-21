package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"chat-microservice/pkg/models"

	"go.mongodb.org/mongo-driver/v2/bson"
)

var (
	ErrNotParticipant = errors.New("user is not a participant of the channel")
	ErrEmptyContent   = errors.New("content is required")
	ErrOverloaded     = errors.New("persistence queue is full")
	ErrClosed         = errors.New("service is shutting down")
)

type Store interface {
	SaveBatch(ctx context.Context, msgs []*models.Message) error
	History(ctx context.Context, participants []string, page, size int) ([]*models.Message, error)
}

type Broadcaster interface {
	Broadcast(recipients []string, payload []byte) int
}

type Options struct {
	Workers      int
	QueueSize    int
	MaxBatch     int
	MaxRetries   int
	RetryBackoff time.Duration
	SaveTimeout  time.Duration
}

func DefaultOptions(maxRetries int) Options {
	return Options{
		Workers:      4,
		QueueSize:    1024,
		MaxBatch:     256,
		MaxRetries:   maxRetries,
		RetryBackoff: 100 * time.Millisecond,
		SaveTimeout:  5 * time.Second,
	}
}

// ChatService delivers messages to connected participants immediately and
// persists them asynchronously through a bounded queue drained by workers, each
// writing whatever has queued up since its last write in a single batch.
type ChatService struct {
	store Store
	hub   Broadcaster
	opts  Options

	mu      sync.RWMutex
	closed  bool
	queue   chan *models.Message
	workers sync.WaitGroup
}

func NewChatService(store Store, hub Broadcaster, opts Options) *ChatService {
	s := &ChatService{store: store, hub: hub, opts: opts, queue: make(chan *models.Message, opts.QueueSize)}
	for range opts.Workers {
		s.workers.Go(s.persist)
	}
	return s
}

// Send validates and queues a message for persistence, then broadcasts it to
// every participant except the sender. It never blocks: when the persistence
// queue is full it fails with ErrOverloaded and nothing is delivered.
func (s *ChatService) Send(sender string, participants []string, content string) (*models.Message, error) {
	participants, err := models.NormalizeParticipants(participants)
	if err != nil {
		return nil, err
	}
	if !models.ContainsUser(participants, sender) {
		return nil, ErrNotParticipant
	}
	if strings.TrimSpace(content) == "" {
		return nil, ErrEmptyContent
	}

	msg := &models.Message{
		ID:           bson.NewObjectID(),
		Sender:       sender,
		Content:      content,
		CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
		Participants: participants,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	if err := s.enqueue(msg); err != nil {
		return nil, err
	}
	s.hub.Broadcast(recipients(participants, sender), payload)
	return msg, nil
}

func (s *ChatService) enqueue(msg *models.Message) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrClosed
	}
	select {
	case s.queue <- msg:
		return nil
	default:
		return ErrOverloaded
	}
}

func recipients(participants []string, sender string) []string {
	out := make([]string, 0, len(participants)-1)
	for _, p := range participants {
		if p != sender {
			out = append(out, p)
		}
	}
	return out
}

func (s *ChatService) History(ctx context.Context, userID string, participants []string, page, size int) ([]*models.Message, error) {
	participants, err := models.NormalizeParticipants(participants)
	if err != nil {
		return nil, err
	}
	if !models.ContainsUser(participants, userID) {
		return nil, ErrNotParticipant
	}
	return s.store.History(ctx, participants, page, size)
}

func (s *ChatService) persist() {
	batch := make([]*models.Message, 0, s.opts.MaxBatch)
	for msg := range s.queue {
		batch = append(batch[:0], msg)
		batch = s.drain(batch)
		s.save(batch)
		clear(batch)
	}
}

// drain appends already queued messages to batch without waiting for more.
func (s *ChatService) drain(batch []*models.Message) []*models.Message {
	for len(batch) < s.opts.MaxBatch {
		select {
		case msg, ok := <-s.queue:
			if !ok {
				return batch
			}
			batch = append(batch, msg)
		default:
			return batch
		}
	}
	return batch
}

func (s *ChatService) save(batch []*models.Message) {
	var err error
	for attempt := 1; attempt <= s.opts.MaxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), s.opts.SaveTimeout)
		err = s.store.SaveBatch(ctx, batch)
		cancel()
		if err == nil {
			return
		}
		slog.Warn("failed to save messages", "count", len(batch), "attempt", attempt, "max", s.opts.MaxRetries, "error", err)
		if attempt < s.opts.MaxRetries {
			time.Sleep(time.Duration(attempt) * s.opts.RetryBackoff)
		}
	}
	slog.Error("dropping messages after retries", "count", len(batch), "error", err)
}

// Close stops accepting messages and waits until every queued message has been
// persisted or ctx expires.
func (s *ChatService) Close(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.queue)
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
