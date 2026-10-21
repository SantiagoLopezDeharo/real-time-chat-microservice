package models

import (
	"errors"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const MaxParticipants = 1000

var (
	ErrNoParticipants      = errors.New("participants array is required")
	ErrEmptyParticipant    = errors.New("participant IDs must not be empty")
	ErrTooManyParticipants = errors.New("too many participants")
)

type Message struct {
	ID           bson.ObjectID `json:"id" bson:"_id"`
	Sender       string        `json:"sender" bson:"sender"`
	Content      string        `json:"content" bson:"content"`
	CreatedAt    time.Time     `json:"created_at" bson:"created_at"`
	Participants []string      `json:"participants" bson:"participants"` // Sorted array of user IDs
}

// NormalizeParticipants trims, de-duplicates and sorts participant IDs so the
// same set of users always identifies the same channel.
func NormalizeParticipants(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, ErrNoParticipants
	}
	if len(raw) > MaxParticipants {
		return nil, ErrTooManyParticipants
	}
	participants := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, ErrEmptyParticipant
		}
		participants = append(participants, p)
	}
	slices.Sort(participants)
	return slices.Compact(participants), nil
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
