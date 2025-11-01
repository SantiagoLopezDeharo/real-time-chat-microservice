package models

import (
	"sort"
	"strings"
	"time"
)

type Message struct {
	ID           string    `json:"id,omitempty" bson:"_id,omitempty"`
	Sender       string    `json:"sender" bson:"sender"`
	Content      string    `json:"content" bson:"content"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
	Participants []string  `json:"participants" bson:"participants"` // Sorted array of user IDs
}

// GetChannelID returns a consistent string representation of the channel
// by joining sorted participant IDs
func (m *Message) GetChannelID() string {
	return strings.Join(m.Participants, ",")
}

// CreateChannelID creates a consistent channel ID from participant IDs
func CreateChannelID(participants []string) string {
	sorted := make([]string, len(participants))
	copy(sorted, participants)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

// ParseChannelID converts a channel ID string back to sorted participants array
func ParseChannelID(channelID string) []string {
	if channelID == "" {
		return []string{}
	}
	participants := strings.Split(channelID, ",")
	sort.Strings(participants)
	return participants
}

// ContainsUser checks if a user is part of the channel
func ContainsUser(participants []string, userID string) bool {
	for _, p := range participants {
		if p == userID {
			return true
		}
	}
	return false
}
