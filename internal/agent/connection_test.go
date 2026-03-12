package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestPoolHandleWebSocket(t *testing.T) {
	var receivedResponse AgentResponse
	var responseMu sync.Mutex

	pool := NewPool(PoolConfig{
		AuthToken: "test-token",
		Workspace: "test-workspace",
		OnResponse: func(agentID string, resp AgentResponse) {
			responseMu.Lock()
			receivedResponse = resp
			responseMu.Unlock()
		},
	})

	pool.RegisterAgent(Info{
		ID:   "pm-001",
		Name: "PM",
		Role: "PM",
	})

	// Start test server
	server := httptest.NewServer(http.HandlerFunc(pool.HandleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	// Connect as coco agent
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Send auth
	auth := AuthRequest{
		Type:    "auth",
		AgentID: "pm-001",
		Token:   "test-token",
	}
	if err := conn.WriteJSON(auth); err != nil {
		t.Fatalf("write auth: %v", err)
	}

	// Read auth response
	var authResp AuthResponse
	if err := conn.ReadJSON(&authResp); err != nil {
		t.Fatalf("read auth response: %v", err)
	}

	if !authResp.Success {
		t.Fatalf("auth failed: %s", authResp.Error)
	}
	if authResp.Workspace != "test-workspace" {
		t.Errorf("workspace = %q, want test-workspace", authResp.Workspace)
	}

	// Verify agent is online
	if !pool.IsOnline("pm-001") {
		t.Error("pm-001 should be online after auth")
	}

	agents := pool.ListAgents()
	found := false
	for _, a := range agents {
		if a.Info.ID == "pm-001" && a.Status == StatusOnline {
			found = true
			break
		}
	}
	if !found {
		t.Error("pm-001 not found in online agents list")
	}

	// Test sending a message to the agent
	msg := RoutedMessage{
		Type:      "message",
		ID:        "msg-001",
		Platform:  "feishu",
		ChannelID: "chat-001",
		UserID:    "user-001",
		Username:  "Test User",
		Text:      "Hello PM",
	}
	if err := pool.SendToAgent("pm-001", msg); err != nil {
		t.Fatalf("send to agent: %v", err)
	}

	// Read the message on the agent side
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	var received RoutedMessage
	if err := json.Unmarshal(raw, &received); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if received.Text != "Hello PM" {
		t.Errorf("received text = %q, want Hello PM", received.Text)
	}

	// Test agent sending a response
	response := AgentResponse{
		Type:      "response",
		MessageID: "msg-001",
		Platform:  "feishu",
		ChannelID: "chat-001",
		Text:      "Hi, I'm PM",
	}
	if err := conn.WriteJSON(response); err != nil {
		t.Fatalf("write response: %v", err)
	}

	// Wait for response handler
	time.Sleep(100 * time.Millisecond)
	responseMu.Lock()
	if receivedResponse.Text != "Hi, I'm PM" {
		t.Errorf("response text = %q, want Hi, I'm PM", receivedResponse.Text)
	}
	responseMu.Unlock()
}

func TestPoolAuthFailure(t *testing.T) {
	pool := NewPool(PoolConfig{
		AuthToken: "correct-token",
		Workspace: "test",
	})

	pool.RegisterAgent(Info{ID: "pm-001", Name: "PM", Role: "PM"})

	server := httptest.NewServer(http.HandlerFunc(pool.HandleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	t.Run("wrong token", func(t *testing.T) {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()

		_ = conn.WriteJSON(AuthRequest{Type: "auth", AgentID: "pm-001", Token: "wrong"})

		var resp AuthResponse
		_ = conn.ReadJSON(&resp)
		if resp.Success {
			t.Error("expected auth to fail with wrong token")
		}
	})

	t.Run("unknown agent", func(t *testing.T) {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()

		_ = conn.WriteJSON(AuthRequest{Type: "auth", AgentID: "unknown", Token: "correct-token"})

		var resp AuthResponse
		_ = conn.ReadJSON(&resp)
		if resp.Success {
			t.Error("expected auth to fail with unknown agent")
		}
	})
}

func TestPoolSendToOfflineAgent(t *testing.T) {
	pool := NewPool(PoolConfig{Workspace: "test"})
	pool.RegisterAgent(Info{ID: "pm-001", Name: "PM", Role: "PM"})

	err := pool.SendToAgent("pm-001", RoutedMessage{Type: "message", Text: "hello"})
	if err == nil {
		t.Error("expected error sending to offline agent")
	}
}
