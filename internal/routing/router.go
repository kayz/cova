// Package routing resolves incoming channel messages to target agents
// and dispatches agent responses back to the originating channel.
package routing

import (
	"context"
	"fmt"
	"log"

	"cova/internal/agent"
	"cova/internal/channel"
)

// Router connects channels to agents via the agent pool.
type Router struct {
	pool     *agent.Pool
	channels map[string]channel.Channel // platform name → channel
}

// New creates a new message router.
func New(pool *agent.Pool) *Router {
	return &Router{
		pool:     pool,
		channels: make(map[string]channel.Channel),
	}
}

// RegisterChannel adds a channel to the router and wires up the message handler.
func (r *Router) RegisterChannel(ch channel.Channel) {
	r.channels[ch.Name()] = ch
	ch.SetMessageHandler(func(msg channel.IncomingMessage) {
		r.routeIncoming(msg)
	})
	log.Printf("[router] registered channel: %s", ch.Name())
}

// HandleAgentResponse is called when a coco agent sends a response.
// It sends the response back through the appropriate channel.
func (r *Router) HandleAgentResponse(agentID string, resp agent.AgentResponse) {
	ch, ok := r.channels[resp.Platform]
	if !ok {
		log.Printf("[router] no channel for platform %q (agent %s)", resp.Platform, agentID)
		return
	}

	// Resolve agent name for display
	agentName := agentID
	if state, ok := r.pool.GetState(agentID); ok {
		agentName = state.Info.Name
	}

	outMsg := channel.OutgoingMessage{
		Platform:  resp.Platform,
		ChannelID: resp.ChannelID,
		Text:      resp.Text,
		AgentID:   agentID,
		AgentName: agentName,
	}

	ctx := context.Background()
	if err := ch.Send(ctx, outMsg); err != nil {
		log.Printf("[router] failed to send response from agent %s to %s/%s: %v",
			agentID, resp.Platform, resp.ChannelID, err)
	}
}

// routeIncoming determines which agent(s) should receive an incoming message
// and forwards it via the agent pool.
func (r *Router) routeIncoming(msg channel.IncomingMessage) {
	targets := r.resolveTargets(msg)
	if len(targets) == 0 {
		log.Printf("[router] no target agent for message %s from %s/%s",
			msg.ID, msg.Platform, msg.UserID)
		return
	}

	routed := agent.RoutedMessage{
		Type:      "message",
		ID:        msg.ID,
		Platform:  msg.Platform,
		ChannelID: msg.ChannelID,
		UserID:    msg.UserID,
		Username:  msg.Username,
		Text:      msg.Text,
		ThreadID:  msg.ThreadID,
		Metadata:  msg.Metadata,
	}

	for _, targetID := range targets {
		if !r.pool.IsOnline(targetID) {
			log.Printf("[router] agent %s is offline, cannot deliver message %s", targetID, msg.ID)
			// Optionally send "agent offline" message back to channel
			r.sendOfflineNotice(msg, targetID)
			continue
		}

		if err := r.pool.SendToAgent(targetID, routed); err != nil {
			log.Printf("[router] failed to route message %s to agent %s: %v", msg.ID, targetID, err)
		} else {
			log.Printf("[router] routed message %s → agent %s", msg.ID, targetID)
		}
	}
}

// resolveTargets determines which agents should receive a message.
//
// Routing rules:
// 1. If the message explicitly @mentions specific agents, route to those.
// 2. If it's a DM (p2p), route to the default agent (PM preferred).
// 3. If it's a group message with no specific mention, ignore (avoid noise).
func (r *Router) resolveTargets(msg channel.IncomingMessage) []string {
	// Rule 1: Explicit @mentions
	if len(msg.Mentions) > 0 {
		return msg.Mentions
	}

	// Rule 2: DM → route to default agent (PM priority, even if offline)
	if msg.ChatType == "p2p" {
		return r.findDefaultAgent()
	}

	// Rule 3: Group message without @mention
	// For now, ignore unmentioned group messages to avoid noise.
	// Agents should be explicitly @mentioned in groups.
	return nil
}

// findDefaultAgent returns the PM agent (preferred), or the first registered agent.
// Returns the agent even if offline so the caller can send an offline notice.
func (r *Router) findDefaultAgent() []string {
	agents := r.pool.ListAgents()

	// Prefer online PM
	for _, s := range agents {
		if s.Info.Role == "PM" && s.Status == agent.StatusOnline {
			return []string{s.Info.ID}
		}
	}

	// Fallback: first online agent
	for _, s := range agents {
		if s.Status == agent.StatusOnline {
			return []string{s.Info.ID}
		}
	}

	// No one online — return PM anyway (offline notice will be sent)
	for _, s := range agents {
		if s.Info.Role == "PM" {
			return []string{s.Info.ID}
		}
	}

	// Last resort: first registered agent
	for _, s := range agents {
		return []string{s.Info.ID}
	}
	return nil
}

func (r *Router) sendOfflineNotice(msg channel.IncomingMessage, agentID string) {
	ch, ok := r.channels[msg.Platform]
	if !ok {
		return
	}

	agentName := agentID
	if state, ok := r.pool.GetState(agentID); ok {
		agentName = state.Info.Name
	}

	notice := channel.OutgoingMessage{
		Platform:  msg.Platform,
		ChannelID: msg.ChannelID,
		Text:      fmt.Sprintf("[workspace] %s 当前不在线，消息将在其上线后投递。", agentName),
	}

	ctx := context.Background()
	if err := ch.Send(ctx, notice); err != nil {
		log.Printf("[router] failed to send offline notice: %v", err)
	}
}
