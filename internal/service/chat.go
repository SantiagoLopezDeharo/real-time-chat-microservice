package service

import (
	"encoding/json"

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
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}

	broadcastMessage := &ws.BroadcastMessage{
		ChannelID: m.ChannelID,
		Message:   b,
	}

	s.hub.Broadcast <- broadcastMessage

	s.repo.SaveAsync(m, s.maxRetries)

	return nil
}

func (s *ChatService) GetMessagesForChannel(channelID string, userID string, groups []string) ([]*models.Message, error) {
	allIDs := append([]string{userID}, groups...)

	for _, id := range allIDs {
		if id == channelID {
			return s.repo.GetMessagesByChannel(channelID)
		}
	}

	return s.repo.GetMessagesForUser(userID, groups)
}
