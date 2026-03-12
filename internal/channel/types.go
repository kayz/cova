// Package channel defines the interface for messaging platform integrations.
//
// A Channel connects cova workspace to an external messaging platform
// (Feishu, WeChat Work, Slack, etc.) and converts platform-specific events
// into a unified IncomingMessage format.
package channel

import "context"

// IncomingMessage is a platform-agnostic message received from a channel.
type IncomingMessage struct {
	ID        string            `json:"id"`
	Platform  string            `json:"platform"`
	ChannelID string            `json:"channel_id"`
	UserID    string            `json:"user_id"`
	Username  string            `json:"username"`
	Text      string            `json:"text"`
	ThreadID  string            `json:"thread_id,omitempty"`
	Mentions  []string          `json:"mentions,omitempty"`  // agent IDs mentioned
	ChatType  string            `json:"chat_type,omitempty"` // "p2p", "group"
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// OutgoingMessage is a platform-agnostic message to send via a channel.
type OutgoingMessage struct {
	Platform  string `json:"platform"`
	ChannelID string `json:"channel_id"`
	Text      string `json:"text"`
	ReplyToID string `json:"reply_to_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`   // who is sending
	AgentName string `json:"agent_name,omitempty"` // display name
}

// MessageHandler is called when a channel receives a message.
type MessageHandler func(msg IncomingMessage)

// Channel is the interface for a messaging platform integration.
type Channel interface {
	// Name returns the platform identifier (e.g., "feishu", "wecom").
	Name() string

	// Start begins listening for events from the platform.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the channel.
	Stop() error

	// Send sends a message through this channel.
	Send(ctx context.Context, msg OutgoingMessage) error

	// SetMessageHandler sets the callback for incoming messages.
	SetMessageHandler(handler MessageHandler)
}
