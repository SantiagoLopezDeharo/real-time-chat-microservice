package service

import (
	"encoding/json"
	"log"
	"sort"
	"time"

	"chat-microservice/internal/repository"
	"chat-microservice/internal/ws"
	"chat-microservice/pkg/models"
)

type ChatService struct {
	repo             repository.Repository
	hub              *ws.Hub
	maxRetries       int
	dbWriteQueue     chan *models.Message
	numDBWokers      int
	numDBJobQueue    int
	dbWriteStopQueue chan bool
}

func NewChatService(repo repository.Repository, hub *ws.Hub, maxRetries int) *ChatService {
	s := &ChatService{
		repo:             repo,
		hub:              hub,
		maxRetries:       maxRetries,
		dbWriteQueue:     make(chan *models.Message, 1024),
		numDBWokers:      4,
		numDBJobQueue:    1024,
		dbWriteStopQueue: make(chan bool),
	}

	for i := 0; i < s.numDBWokers; i++ {
		go s.dbWorker()
	}

	return s
}

func (s *ChatService) dbWorker() {
	log.Println("DB worker started")
	for {
		select {
		case msg := <-s.dbWriteQueue:
			var lastErr error
			for attempt := 1; attempt <= s.maxRetries; attempt++ {
				err := s.repo.Save(msg)
				if err == nil {
					break
				}
				lastErr = err
				log.Printf("failed to save message (attempt %d/%d): %v", attempt, s.maxRetries, err)
				if attempt < s.maxRetries {
					time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
				}
			}
			if lastErr != nil {
				log.Printf("failed to save message after %d attempts: %v", s.maxRetries, lastErr)
			}
		case <-s.dbWriteStopQueue:
			log.Println("DB worker stopped")
			return
		}
	}
}

func (s *ChatService) Stop() {
	close(s.dbWriteStopQueue)
}

func (s *ChatService) Hub() *ws.Hub { return s.hub }

func (s *ChatService) BroadcastMessage(m *models.Message) error {
	// Sort participants to ensure consistency
	sort.Strings(m.Participants)

	b, err := json.Marshal(m)
	if err != nil {
		return err
	}

	broadcastMessage := &ws.BroadcastMessage{
		Participants: m.Participants,
		Message:      b,
		SenderID:     m.Sender,
	}

	s.hub.Broadcast <- broadcastMessage

	s.dbWriteQueue <- m

	return nil
}

func (s *ChatService) GetMessagesForChannel(participants []string, userID string) ([]*models.Message, error) {
	if !models.ContainsUser(participants, userID) {
		return []*models.Message{}, nil
	}

	sort.Strings(participants)

	return s.repo.GetMessagesByParticipants(participants)
}

func (s *ChatService) GetMessagesForChannelWithPagination(participants []string, userID string, page int, size int) ([]*models.Message, error) {
	if !models.ContainsUser(participants, userID) {
		return []*models.Message{}, nil
	}

	sort.Strings(participants)

	return s.repo.GetMessagesByParticipantsWithPagination(participants, page, size)
}
