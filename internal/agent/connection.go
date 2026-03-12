package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 15 * time.Second
)

// Conn wraps a WebSocket connection to a coco agent.
type Conn struct {
	ws        *websocket.Conn
	agentID   string
	sessionID string
	writeMu   sync.Mutex
	closeCh   chan struct{}
	closed    bool
}

// Send sends a JSON message to the connected coco agent.
func (c *Conn) Send(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return fmt.Errorf("connection closed for agent %s", c.agentID)
	}
	_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
	return c.ws.WriteJSON(v)
}

// Close terminates the connection.
func (c *Conn) Close() {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	close(c.closeCh)
	_ = c.ws.Close()
}

// ResponseHandler is called when a coco agent sends a response message.
type ResponseHandler func(agentID string, resp AgentResponse)

// Pool manages WebSocket connections from coco agents.
type Pool struct {
	mu          sync.RWMutex
	conns       map[string]*Conn    // agentID → active connection
	agents      map[string]Info     // agentID → static info
	states      map[string]*State   // agentID → runtime state
	authToken   string              // shared auth token for agents
	workspace   string              // workspace/project name
	onResponse  ResponseHandler
	upgrader    websocket.Upgrader
}

// PoolConfig configures the connection pool.
type PoolConfig struct {
	AuthToken  string
	Workspace  string
	OnResponse ResponseHandler
}

// NewPool creates a new agent connection pool.
func NewPool(cfg PoolConfig) *Pool {
	return &Pool{
		conns:      make(map[string]*Conn),
		agents:     make(map[string]Info),
		states:     make(map[string]*State),
		authToken:  cfg.AuthToken,
		workspace:  cfg.Workspace,
		onResponse: cfg.OnResponse,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// RegisterAgent adds a known agent to the pool (from registry).
func (p *Pool) RegisterAgent(info Info) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.agents[info.ID] = info
	p.states[info.ID] = &State{
		Info:   info,
		Status: StatusOffline,
	}
}

// HandleWebSocket is the HTTP handler for agent WebSocket connections.
func (p *Pool) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	ws, err := p.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[pool] websocket upgrade failed: %v", err)
		return
	}

	// Read auth message
	_ = ws.SetReadDeadline(time.Now().Add(30 * time.Second))
	var authReq AuthRequest
	if err := ws.ReadJSON(&authReq); err != nil {
		log.Printf("[pool] failed to read auth message: %v", err)
		_ = ws.WriteJSON(AuthResponse{Type: "auth_result", Success: false, Error: "invalid auth message"})
		ws.Close()
		return
	}

	if authReq.Type != "auth" {
		_ = ws.WriteJSON(AuthResponse{Type: "auth_result", Success: false, Error: "expected auth message"})
		ws.Close()
		return
	}

	// Validate token
	if p.authToken != "" && authReq.Token != p.authToken {
		_ = ws.WriteJSON(AuthResponse{Type: "auth_result", Success: false, Error: "invalid token"})
		ws.Close()
		return
	}

	// Validate agent is registered
	p.mu.RLock()
	info, known := p.agents[authReq.AgentID]
	p.mu.RUnlock()

	if !known {
		_ = ws.WriteJSON(AuthResponse{Type: "auth_result", Success: false, Error: fmt.Sprintf("unknown agent_id: %s", authReq.AgentID)})
		ws.Close()
		return
	}

	sessionID := fmt.Sprintf("sess_%s_%d", authReq.AgentID, time.Now().UnixMilli())

	conn := &Conn{
		ws:        ws,
		agentID:   authReq.AgentID,
		sessionID: sessionID,
		closeCh:   make(chan struct{}),
	}

	// Close any existing connection for this agent
	p.mu.Lock()
	if old, ok := p.conns[authReq.AgentID]; ok {
		old.Close()
	}
	p.conns[authReq.AgentID] = conn
	p.states[authReq.AgentID] = &State{
		Info:        info,
		Status:      StatusOnline,
		ConnectedAt: time.Now(),
		LastSeenAt:  time.Now(),
		SessionID:   sessionID,
		RemoteAddr:  r.RemoteAddr,
	}
	p.mu.Unlock()

	// Send auth success
	authResp := AuthResponse{
		Type:      "auth_result",
		Success:   true,
		SessionID: sessionID,
		Workspace: p.workspace,
		Tools:     info.Tools,
	}
	if err := conn.Send(authResp); err != nil {
		log.Printf("[pool] failed to send auth response to %s: %v", authReq.AgentID, err)
		conn.Close()
		return
	}

	log.Printf("[pool] agent %s (%s) connected, session=%s", authReq.AgentID, info.Name, sessionID)

	// Start read loop and ping loop
	go p.readLoop(conn)
	go p.pingLoop(conn)
}

// SendToAgent sends a message to a specific connected agent.
func (p *Pool) SendToAgent(agentID string, msg RoutedMessage) error {
	p.mu.RLock()
	conn, ok := p.conns[agentID]
	p.mu.RUnlock()

	if !ok {
		return fmt.Errorf("agent %s is not connected", agentID)
	}
	return conn.Send(msg)
}

// GetState returns the runtime state of an agent.
func (p *Pool) GetState(agentID string) (State, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.states[agentID]
	if !ok {
		return State{}, false
	}
	return *s, true
}

// ListAgents returns the state of all registered agents.
func (p *Pool) ListAgents() []State {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]State, 0, len(p.states))
	for _, s := range p.states {
		out = append(out, *s)
	}
	return out
}

// IsOnline reports whether an agent is connected.
func (p *Pool) IsOnline(agentID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.states[agentID]
	return ok && s.Status == StatusOnline
}

func (p *Pool) readLoop(conn *Conn) {
	defer p.disconnectAgent(conn.agentID)

	_ = conn.ws.SetReadDeadline(time.Now().Add(pongWait))
	conn.ws.SetPongHandler(func(string) error {
		_ = conn.ws.SetReadDeadline(time.Now().Add(pongWait))
		p.mu.Lock()
		if s, ok := p.states[conn.agentID]; ok {
			s.LastSeenAt = time.Now()
		}
		p.mu.Unlock()
		return nil
	})

	for {
		_, raw, err := conn.ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[pool] agent %s read error: %v", conn.agentID, err)
			}
			return
		}

		// Parse message type
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			log.Printf("[pool] agent %s invalid message: %v", conn.agentID, err)
			continue
		}

		switch envelope.Type {
		case "response":
			var resp AgentResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				log.Printf("[pool] agent %s invalid response: %v", conn.agentID, err)
				continue
			}
			if p.onResponse != nil {
				p.onResponse(conn.agentID, resp)
			}
		default:
			log.Printf("[pool] agent %s unknown message type: %s", conn.agentID, envelope.Type)
		}
	}
}

func (p *Pool) pingLoop(conn *Conn) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			conn.writeMu.Lock()
			_ = conn.ws.SetWriteDeadline(time.Now().Add(writeWait))
			err := conn.ws.WriteMessage(websocket.PingMessage, nil)
			conn.writeMu.Unlock()
			if err != nil {
				return
			}
		case <-conn.closeCh:
			return
		}
	}
}

func (p *Pool) disconnectAgent(agentID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if conn, ok := p.conns[agentID]; ok {
		conn.Close()
		delete(p.conns, agentID)
	}
	if s, ok := p.states[agentID]; ok {
		s.Status = StatusOffline
	}
	log.Printf("[pool] agent %s disconnected", agentID)
}
