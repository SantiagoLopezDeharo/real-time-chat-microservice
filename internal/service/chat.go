package service

import (
	"encoding/json"
	"sort"

	"chat-microservice/internal/repository"
	"chat-microservice/internal/ws"
	"chat-microservice/pkg/models"
)

type ChatService struct {
	repo       repository.Repository
	hub        *ws.Hub
	maxRetries int
}

func NewChatService(repo repository.Repository, hub *ws.Hub, maxRetries int) *ChatService {
	return &ChatService{
		repo:       repo,
		hub:        hub,
		maxRetries: maxRetries,
	}
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

	s.repo.SaveAsync(m, s.maxRetries)

	return nil
}

// GetMessagesForChannel returns all messages for a channel if the user is a participant
func (s *ChatService) GetMessagesForChannel(participants []string, userID string) ([]*models.Message, error) {
	// Check if user is part of the channel
	if !models.ContainsUser(participants, userID) {
		return []*models.Message{}, nil
	}

	// Sort participants for consistent lookup
	sort.Strings(participants)

	return s.repo.GetMessagesByParticipants(participants)
}

// GetMessagesForChannelWithPagination returns paginated messages for a channel if the user is a participant
// page: page number (0-indexed)
// size: number of messages per page
func (s *ChatService) GetMessagesForChannelWithPagination(participants []string, userID string, page int, size int) ([]*models.Message, error) {
	// Check if user is part of the channel
	if !models.ContainsUser(participants, userID) {
		return []*models.Message{}, nil
	}

	// Sort participants for consistent lookup
	sort.Strings(participants)

	return s.repo.GetMessagesByParticipantsWithPagination(participants, page, size)
}
