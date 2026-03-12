package routing

import (
	"context"
	"sync"
	"testing"

	"cova/internal/agent"
	"cova/internal/channel"
)

// mockChannel implements channel.Channel for testing.
type mockChannel struct {
	name     string
	handler  channel.MessageHandler
	sentMsgs []channel.OutgoingMessage
	mu       sync.Mutex
}

func (m *mockChannel) Name() string { return m.name }
func (m *mockChannel) Start(_ context.Context) error { return nil }
func (m *mockChannel) Stop() error { return nil }
func (m *mockChannel) SetMessageHandler(h channel.MessageHandler) { m.handler = h }
func (m *mockChannel) Send(_ context.Context, msg channel.OutgoingMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentMsgs = append(m.sentMsgs, msg)
	return nil
}
func (m *mockChannel) getSentMessages() []channel.OutgoingMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]channel.OutgoingMessage, len(m.sentMsgs))
	copy(out, m.sentMsgs)
	return out
}

func TestRouterMentionRouting(t *testing.T) {
	var receivedMsgs []agent.RoutedMessage
	var mu sync.Mutex

	pool := agent.NewPool(agent.PoolConfig{
		Workspace: "test",
	})
	pool.RegisterAgent(agent.Info{ID: "pm-001", Name: "PM", Role: "PM"})
	pool.RegisterAgent(agent.Info{ID: "ba-001", Name: "BA", Role: "BA"})

	router := New(pool)

	ch := &mockChannel{name: "feishu"}
	router.RegisterChannel(ch)

	// Simulate: agent pm-001 is not connected, so routing should send offline notice
	ch.handler(channel.IncomingMessage{
		ID:        "msg-1",
		Platform:  "feishu",
		ChannelID: "chat-1",
		UserID:    "user-1",
		Username:  "Human",
		Text:      "Hello PM",
		ChatType:  "group",
		Mentions:  []string{"pm-001"},
	})

	// Should have sent offline notice since pm-001 is not connected
	sent := ch.getSentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 offline notice, got %d", len(sent))
	}
	if sent[0].ChannelID != "chat-1" {
		t.Errorf("offline notice channel = %q, want chat-1", sent[0].ChannelID)
	}

	_ = receivedMsgs
	_ = mu
}

func TestRouterDMRouting(t *testing.T) {
	pool := agent.NewPool(agent.PoolConfig{Workspace: "test"})
	pool.RegisterAgent(agent.Info{ID: "pm-001", Name: "PM", Role: "PM"})

	router := New(pool)
	ch := &mockChannel{name: "feishu"}
	router.RegisterChannel(ch)

	// DM without @mention should route to default agent (PM)
	// But pm-001 is offline, so should get offline notice
	ch.handler(channel.IncomingMessage{
		ID:       "msg-2",
		Platform: "feishu",
		ChannelID: "dm-1",
		UserID:   "user-1",
		Text:     "Hi",
		ChatType: "p2p",
	})

	sent := ch.getSentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 offline notice for DM, got %d", len(sent))
	}
}

func TestRouterGroupNoMention(t *testing.T) {
	pool := agent.NewPool(agent.PoolConfig{Workspace: "test"})
	pool.RegisterAgent(agent.Info{ID: "pm-001", Name: "PM", Role: "PM"})

	router := New(pool)
	ch := &mockChannel{name: "feishu"}
	router.RegisterChannel(ch)

	// Group message without @mention should be ignored
	ch.handler(channel.IncomingMessage{
		ID:       "msg-3",
		Platform: "feishu",
		ChannelID: "group-1",
		UserID:   "user-1",
		Text:     "just chatting",
		ChatType: "group",
	})

	sent := ch.getSentMessages()
	if len(sent) != 0 {
		t.Errorf("expected no messages for unmentioned group chat, got %d", len(sent))
	}
}

func TestRouterAgentResponse(t *testing.T) {
	pool := agent.NewPool(agent.PoolConfig{Workspace: "test"})
	pool.RegisterAgent(agent.Info{ID: "pm-001", Name: "老王", Role: "PM"})

	router := New(pool)
	ch := &mockChannel{name: "feishu"}
	router.RegisterChannel(ch)

	// Simulate agent response
	router.HandleAgentResponse("pm-001", agent.AgentResponse{
		Type:      "response",
		MessageID: "msg-1",
		Platform:  "feishu",
		ChannelID: "chat-1",
		Text:      "收到，我来处理",
	})

	sent := ch.getSentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sent))
	}
	if sent[0].Text != "收到，我来处理" {
		t.Errorf("sent text = %q, want 收到，我来处理", sent[0].Text)
	}
	if sent[0].AgentName != "老王" {
		t.Errorf("agent name = %q, want 老王", sent[0].AgentName)
	}
}
