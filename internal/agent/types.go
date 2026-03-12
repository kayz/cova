// Package agent manages coco agent registrations, connections, and lifecycles.
//
// In the cova workspace model, each coco instance is an independent agent
// (team member) that connects via WebSocket, receives routed messages,
// and sends responses back through the workspace.
package agent

import "time"

// Status represents the current state of an agent.
type Status string

const (
	StatusOffline  Status = "offline"
	StatusOnline   Status = "online"
	StatusBusy     Status = "busy"
	StatusIdle     Status = "idle"
	StatusArchived Status = "archived"
)

// Info holds the static definition of an agent loaded from registry.
type Info struct {
	ID          string   `yaml:"agent_id"    json:"agent_id"`
	Name        string   `yaml:"name"        json:"name"`
	Role        string   `yaml:"role"        json:"role"`
	Description string   `yaml:"description" json:"description"`
	AgentMDPath string   `yaml:"agent_md"    json:"agent_md,omitempty"`
	Enabled     bool     `yaml:"enabled"     json:"enabled"`
	Tools       []string `yaml:"tools"       json:"tools,omitempty"`
}

// State holds the runtime state of a connected agent.
type State struct {
	Info        Info      `json:"info"`
	Status      Status    `json:"status"`
	ConnectedAt time.Time `json:"connected_at,omitempty"`
	LastSeenAt  time.Time `json:"last_seen_at,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
	RemoteAddr  string    `json:"remote_addr,omitempty"`
}

// AuthRequest is sent by a coco agent on WebSocket connect.
type AuthRequest struct {
	Type          string `json:"type"`           // "auth"
	AgentID       string `json:"agent_id"`       // registered agent ID
	Token         string `json:"token"`          // auth token
	ClientVersion string `json:"client_version"` // coco version
	Role          string `json:"role,omitempty"` // role hint
}

// AuthResponse is returned to the coco agent after authentication.
type AuthResponse struct {
	Type      string   `json:"type"`                // "auth_result"
	Success   bool     `json:"success"`
	SessionID string   `json:"session_id,omitempty"`
	Workspace string   `json:"workspace,omitempty"` // project/workspace name
	Error     string   `json:"error,omitempty"`
	Tools     []string `json:"tools,omitempty"` // available tool names
}

// RoutedMessage is sent from cova to a coco agent via WebSocket.
type RoutedMessage struct {
	Type      string            `json:"type"`       // "message"
	ID        string            `json:"id"`         // message ID
	Platform  string            `json:"platform"`   // originating platform
	ChannelID string            `json:"channel_id"` // originating channel
	UserID    string            `json:"user_id"`    // sender user ID
	Username  string            `json:"username"`   // sender display name
	Text      string            `json:"text"`       // message text
	ThreadID  string            `json:"thread_id,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// AgentResponse is sent from a coco agent back to cova via WebSocket.
type AgentResponse struct {
	Type      string `json:"type"`       // "response"
	MessageID string `json:"message_id"` // original message ID being replied to
	Platform  string `json:"platform"`   // target platform
	ChannelID string `json:"channel_id"` // target channel
	Text      string `json:"text"`       // response text
}
