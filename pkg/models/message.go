package models

import "time"

type Message struct {
	ID        string    `json:"id,omitempty"`
	Sender    string    `json:"sender"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	ChannelID string    `json:"channel_id"`
}
