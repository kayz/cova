package cova

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WorkspaceClient connects a coco agent to a cova workspace.
//
// Usage:
//
//	client, err := cova.NewWorkspaceClient(cova.WorkspaceConfig{
//	    ServerURL: "ws://localhost:18900/ws",
//	    AgentID:   "pm-001",
//	    Token:     "secret",
//	})
//	client.OnMessage(func(msg cova.WorkspaceMessage) {
//	    // handle incoming message
//	})
//	client.Connect()
type WorkspaceClient struct {
	cfg       WorkspaceConfig
	conn      *websocket.Conn
	connMu    sync.Mutex
	writeMu   sync.Mutex
	onMessage func(WorkspaceMessage)
	sessionID string
	workspace string
	tools     []string
	done      chan struct{}
}

// WorkspaceConfig configures the workspace client.
type WorkspaceConfig struct {
	ServerURL     string // WebSocket URL, e.g. "ws://localhost:18900/ws"
	AgentID       string // registered agent ID
	Token         string // auth token
	Role          string // role hint
	ClientVersion string // coco version
}

// WorkspaceMessage is an incoming message routed to this agent.
type WorkspaceMessage struct {
	Type      string            `json:"type"`
	ID        string            `json:"id"`
	Platform  string            `json:"platform"`
	ChannelID string            `json:"channel_id"`
	UserID    string            `json:"user_id"`
	Username  string            `json:"username"`
	Text      string            `json:"text"`
	ThreadID  string            `json:"thread_id,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// WorkspaceResponse is sent from the agent back to the workspace.
type WorkspaceResponse struct {
	Type      string `json:"type"`       // "response"
	MessageID string `json:"message_id"` // original message ID
	Platform  string `json:"platform"`   // target platform
	ChannelID string `json:"channel_id"` // target channel
	Text      string `json:"text"`       // response text
}

type authRequest struct {
	Type          string `json:"type"`
	AgentID       string `json:"agent_id"`
	Token         string `json:"token,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`
	Role          string `json:"role,omitempty"`
}

type authResponse struct {
	Type      string   `json:"type"`
	Success   bool     `json:"success"`
	SessionID string   `json:"session_id,omitempty"`
	Workspace string   `json:"workspace,omitempty"`
	Error     string   `json:"error,omitempty"`
	Tools     []string `json:"tools,omitempty"`
}

// NewWorkspaceClient creates a new workspace client.
func NewWorkspaceClient(cfg WorkspaceConfig) *WorkspaceClient {
	if cfg.ClientVersion == "" {
		cfg.ClientVersion = "0.1.0"
	}
	return &WorkspaceClient{
		cfg:  cfg,
		done: make(chan struct{}),
	}
}

// OnMessage sets the handler for incoming workspace messages.
func (c *WorkspaceClient) OnMessage(handler func(WorkspaceMessage)) {
	c.onMessage = handler
}

// Connect establishes the WebSocket connection and authenticates.
func (c *WorkspaceClient) Connect() error {
	conn, _, err := websocket.DefaultDialer.Dial(c.cfg.ServerURL, nil)
	if err != nil {
		return fmt.Errorf("workspace connect: %w", err)
	}
	c.conn = conn

	// Send auth
	auth := authRequest{
		Type:          "auth",
		AgentID:       c.cfg.AgentID,
		Token:         c.cfg.Token,
		ClientVersion: c.cfg.ClientVersion,
		Role:          c.cfg.Role,
	}
	if err := conn.WriteJSON(auth); err != nil {
		conn.Close()
		return fmt.Errorf("workspace auth send: %w", err)
	}

	// Read auth response
	var resp authResponse
	if err := conn.ReadJSON(&resp); err != nil {
		conn.Close()
		return fmt.Errorf("workspace auth read: %w", err)
	}

	if !resp.Success {
		conn.Close()
		return fmt.Errorf("workspace auth failed: %s", resp.Error)
	}

	c.sessionID = resp.SessionID
	c.workspace = resp.Workspace
	c.tools = resp.Tools

	log.Printf("[workspace-client] connected to %s (session=%s, workspace=%s)",
		c.cfg.ServerURL, c.sessionID, c.workspace)

	// Start read loop
	go c.readLoop()

	return nil
}

// SendResponse sends a response message back through the workspace.
func (c *WorkspaceClient) SendResponse(resp WorkspaceResponse) error {
	resp.Type = "response"
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.conn == nil {
		return fmt.Errorf("not connected")
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteJSON(resp)
}

// Close terminates the connection.
func (c *WorkspaceClient) Close() {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// SessionID returns the session ID assigned by the workspace.
func (c *WorkspaceClient) SessionID() string { return c.sessionID }

// Workspace returns the workspace/project name.
func (c *WorkspaceClient) Workspace() string { return c.workspace }

// Tools returns the list of available tool names.
func (c *WorkspaceClient) Tools() []string { return c.tools }

func (c *WorkspaceClient) readLoop() {
	defer close(c.done)

	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[workspace-client] read error: %v", err)
			}
			return
		}

		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			continue
		}

		switch envelope.Type {
		case "message":
			var msg WorkspaceMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("[workspace-client] invalid message: %v", err)
				continue
			}
			if c.onMessage != nil {
				c.onMessage(msg)
			}
		}
	}
}
