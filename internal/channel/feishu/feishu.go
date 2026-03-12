// Package feishu implements the channel.Channel interface for Feishu/Lark.
//
// It connects to the Feishu open platform via WebSocket event subscription,
// receives messages from users and groups, and sends replies via the Feishu API.
package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"cova/internal/channel"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// Config holds Feishu platform credentials.
type Config struct {
	AppID     string // from Feishu Developer Console
	AppSecret string // from Feishu Developer Console

	// BotNameToAgentID maps bot display names to agent IDs.
	// Each agent in the workspace is registered as a separate Feishu bot.
	// When a message @mentions a bot, we resolve which agent to route to.
	BotNameToAgentID map[string]string
}

// Channel implements channel.Channel for Feishu.
type Channel struct {
	client    *lark.Client
	wsClient  *larkws.Client
	botOpenID string
	handler   channel.MessageHandler
	cfg       Config
	ctx       context.Context
	cancel    context.CancelFunc

	// username cache
	usernameMu sync.RWMutex
	usernames  map[string]string // openID → display name
}

// New creates a new Feishu channel.
func New(cfg Config) (*Channel, error) {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, fmt.Errorf("feishu: AppID and AppSecret are required")
	}

	client := lark.NewClient(cfg.AppID, cfg.AppSecret)

	ch := &Channel{
		client:    client,
		cfg:       cfg,
		usernames: make(map[string]string),
	}

	ch.wsClient = larkws.NewClient(cfg.AppID, cfg.AppSecret,
		larkws.WithEventHandler(ch.buildEventHandler()),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)

	return ch, nil
}

func (ch *Channel) Name() string { return "feishu" }

func (ch *Channel) SetMessageHandler(handler channel.MessageHandler) {
	ch.handler = handler
}

func (ch *Channel) Start(ctx context.Context) error {
	ch.ctx, ch.cancel = context.WithCancel(ctx)

	go func() {
		if err := ch.wsClient.Start(ch.ctx); err != nil {
			log.Printf("[feishu] WebSocket error: %v", err)
		}
	}()

	log.Printf("[feishu] channel started (app_id=%s)", ch.cfg.AppID)
	return nil
}

func (ch *Channel) Stop() error {
	if ch.cancel != nil {
		ch.cancel()
	}
	log.Printf("[feishu] channel stopped")
	return nil
}

func (ch *Channel) Send(ctx context.Context, msg channel.OutgoingMessage) error {
	// Build display text: prepend agent name if available
	text := msg.Text
	if msg.AgentName != "" {
		// Don't double-prefix if the text already starts with the name
		if !strings.HasPrefix(text, msg.AgentName) {
			text = msg.Text
		}
	}

	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("feishu: marshal message: %w", err)
	}

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(msg.ChannelID).
			MsgType(larkim.MsgTypeText).
			Content(string(content)).
			Build()).
		Build()

	result, err := ch.client.Im.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("feishu: send message: %w", err)
	}
	if !result.Success() {
		return fmt.Errorf("feishu: send message: code=%d msg=%s", result.Code, result.Msg)
	}
	return nil
}

func (ch *Channel) buildEventHandler() *dispatcher.EventDispatcher {
	handler := dispatcher.NewEventDispatcher("", "")
	handler.OnP2MessageReceiveV1(ch.handleMessage)
	return handler
}

func (ch *Channel) handleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return nil
	}

	msg := event.Event.Message
	sender := event.Event.Sender

	// Ignore bot's own messages
	if sender != nil && sender.SenderId != nil && ch.botOpenID != "" {
		if *sender.SenderId.OpenId == ch.botOpenID {
			return nil
		}
	}

	// Extract text
	text := ch.extractText(msg)
	text = ch.cleanMention(text)
	if text == "" {
		return nil
	}

	// Determine chat type
	chatType := ""
	if msg.ChatType != nil {
		chatType = *msg.ChatType
	}

	// For group chats, only respond to @mentions
	if chatType == "group" && !ch.isMentioned(msg) {
		return nil
	}

	// Resolve sender info
	userID := ""
	username := ""
	if sender != nil && sender.SenderId != nil {
		userID = *sender.SenderId.OpenId
		username = ch.getUsername(ctx, userID)
	}

	chatID := ""
	if msg.ChatId != nil {
		chatID = *msg.ChatId
	}
	msgID := ""
	if msg.MessageId != nil {
		msgID = *msg.MessageId
	}

	// Resolve which agents are @mentioned
	mentions := ch.resolveMentions(msg)

	if ch.handler != nil {
		ch.handler(channel.IncomingMessage{
			ID:        msgID,
			Platform:  "feishu",
			ChannelID: chatID,
			UserID:    userID,
			Username:  username,
			Text:      text,
			ChatType:  chatType,
			Mentions:  mentions,
			Metadata: map[string]string{
				"chat_type": chatType,
			},
		})
	}

	return nil
}

func (ch *Channel) isMentioned(msg *larkim.EventMessage) bool {
	if msg.Mentions == nil {
		return false
	}
	// Any @mention in the message triggers processing
	return len(msg.Mentions) > 0
}

func (ch *Channel) resolveMentions(msg *larkim.EventMessage) []string {
	if msg.Mentions == nil {
		return nil
	}
	var mentions []string
	for _, m := range msg.Mentions {
		if m.Name == nil {
			continue
		}
		name := *m.Name
		// Check if this mention maps to a known agent
		if agentID, ok := ch.cfg.BotNameToAgentID[name]; ok {
			mentions = append(mentions, agentID)
		}
	}
	return mentions
}

func (ch *Channel) extractText(msg *larkim.EventMessage) string {
	if msg.Content == nil {
		return ""
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(*msg.Content), &content); err != nil {
		return ""
	}
	return content.Text
}

func (ch *Channel) cleanMention(text string) string {
	for {
		start := strings.Index(text, "@_user_")
		if start == -1 {
			break
		}
		end := start + 7
		for end < len(text) && text[end] >= '0' && text[end] <= '9' {
			end++
		}
		text = text[:start] + text[end:]
	}
	return strings.TrimSpace(text)
}

func (ch *Channel) getUsername(ctx context.Context, openID string) string {
	ch.usernameMu.RLock()
	if name, ok := ch.usernames[openID]; ok {
		ch.usernameMu.RUnlock()
		return name
	}
	ch.usernameMu.RUnlock()

	req := larkcontact.NewGetUserReqBuilder().
		UserId(openID).
		UserIdType(larkcontact.UserIdTypeOpenId).
		Build()

	result, err := ch.client.Contact.User.Get(ctx, req)
	if err != nil || !result.Success() {
		return openID
	}

	if result.Data != nil && result.Data.User != nil && result.Data.User.Name != nil {
		name := *result.Data.User.Name
		ch.usernameMu.Lock()
		ch.usernames[openID] = name
		ch.usernameMu.Unlock()
		return name
	}
	return openID
}
