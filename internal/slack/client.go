// Package slack wraps the slack-go Socket Mode + Web API in a small surface so
// the bridge never touches slack-go types directly. Socket Mode keeps a single
// outbound WebSocket, so the agent needs no inbound ingress — mirroring the
// Telegram long-poll posture of the sibling Kubernetes assembly.
package slack

import (
	"context"
	"errors"
	"fmt"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// Button is a Block Kit action button.
type Button struct {
	Text     string
	ActionID string
	Value    string
	// Style is "", "primary", or "danger".
	Style string
}

// MessageEvent is a user message addressed to the bot (DM or mention).
type MessageEvent struct {
	EventID   string
	UserID    string
	ChannelID string
	Text      string
	// ThreadTS is the parent thread timestamp, empty for top-level messages.
	ThreadTS string
	TS       string
	IsDirect bool
}

// SlashCommand is a slash-command invocation (e.g. /aurora new).
type SlashCommand struct {
	EnvelopeID string
	UserID     string
	ChannelID  string
	Command    string
	Text       string
	TriggerID  string
}

// BlockAction is an interactive button click.
type BlockAction struct {
	EnvelopeID string
	UserID     string
	ChannelID  string
	MessageTS  string
	ActionID   string
	Value      string
}

// Handler receives decoded Slack events. Implementations must be safe to call
// from the Socket Mode goroutine.
type Handler interface {
	HandleMessage(ctx context.Context, event MessageEvent)
	HandleSlash(ctx context.Context, command SlashCommand)
	HandleAction(ctx context.Context, action BlockAction)
}

type Client struct {
	api       *slack.Client
	socket    *socketmode.Client
	botUserID string
}

func NewClient(appToken, botToken string) (*Client, error) {
	if appToken == "" || botToken == "" {
		return nil, errors.New("both a Slack app-level token and bot token are required")
	}
	api := slack.New(botToken, slack.OptionAppLevelToken(appToken))
	return &Client{api: api, socket: socketmode.New(api)}, nil
}

// Identify resolves and caches the bot's own user ID via auth.test.
func (c *Client) Identify(ctx context.Context) (string, error) {
	resp, err := c.api.AuthTestContext(ctx)
	if err != nil {
		return "", err
	}
	c.botUserID = resp.UserID
	return resp.UserID, nil
}

func (c *Client) BotUserID() string { return c.botUserID }

// Run consumes the Socket Mode event stream, acking each envelope and routing
// decoded events to the handler. It blocks until ctx is cancelled.
func (c *Client) Run(ctx context.Context, handler Handler) error {
	go c.dispatch(ctx, handler)
	return c.socket.RunContext(ctx)
}

func (c *Client) dispatch(ctx context.Context, handler Handler) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-c.socket.Events:
			if !ok {
				return
			}
			c.route(ctx, handler, event)
		}
	}
}

func (c *Client) route(ctx context.Context, handler Handler, event socketmode.Event) {
	switch event.Type {
	case socketmode.EventTypeEventsAPI:
		payload, ok := event.Data.(slackevents.EventsAPIEvent)
		if ok && event.Request != nil {
			c.socket.Ack(*event.Request)
			c.routeEventsAPI(ctx, handler, payload)
		}
	case socketmode.EventTypeInteractive:
		callback, ok := event.Data.(slack.InteractionCallback)
		if ok && event.Request != nil {
			c.socket.Ack(*event.Request)
			c.routeInteraction(ctx, handler, event.Request.EnvelopeID, callback)
		}
	case socketmode.EventTypeSlashCommand:
		command, ok := event.Data.(slack.SlashCommand)
		if ok && event.Request != nil {
			c.socket.Ack(*event.Request)
			handler.HandleSlash(ctx, SlashCommand{
				EnvelopeID: event.Request.EnvelopeID,
				UserID:     command.UserID,
				ChannelID:  command.ChannelID,
				Command:    command.Command,
				Text:       command.Text,
				TriggerID:  command.TriggerID,
			})
		}
	}
}

func (c *Client) routeEventsAPI(ctx context.Context, handler Handler, payload slackevents.EventsAPIEvent) {
	if payload.Type != slackevents.CallbackEvent {
		return
	}
	switch inner := payload.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		if inner.User == c.botUserID {
			return
		}
		handler.HandleMessage(ctx, MessageEvent{
			EventID: inner.Channel + ":" + inner.TimeStamp, UserID: inner.User, ChannelID: inner.Channel,
			Text: inner.Text, ThreadTS: inner.ThreadTimeStamp, TS: inner.TimeStamp,
		})
	case *slackevents.MessageEvent:
		// Only direct messages from a human; ignore the bot's own posts, edits,
		// and channel chatter (mentions arrive as AppMentionEvent above).
		if inner.BotID != "" || inner.User == "" || inner.User == c.botUserID || inner.SubType != "" {
			return
		}
		if inner.ChannelType != "im" {
			return
		}
		handler.HandleMessage(ctx, MessageEvent{
			EventID: inner.Channel + ":" + inner.TimeStamp, UserID: inner.User, ChannelID: inner.Channel,
			Text: inner.Text, ThreadTS: inner.ThreadTimeStamp, TS: inner.TimeStamp,
			IsDirect: true,
		})
	}
}

func (c *Client) routeInteraction(ctx context.Context, handler Handler, envelopeID string, callback slack.InteractionCallback) {
	if callback.Type != slack.InteractionTypeBlockActions {
		return
	}
	for _, action := range callback.ActionCallback.BlockActions {
		handler.HandleAction(ctx, BlockAction{
			EnvelopeID: envelopeID,
			UserID:     callback.User.ID,
			ChannelID:  callback.Channel.ID,
			MessageTS:  callback.Message.Timestamp,
			ActionID:   action.ActionID,
			Value:      action.Value,
		})
	}
}

// PostMessage posts text (with optional buttons) to a channel, optionally inside
// a thread, returning the new message timestamp.
func (c *Client) PostMessage(ctx context.Context, channelID, threadTS, text string, buttons []Button) (string, error) {
	options := []slack.MsgOption{slack.MsgOptionBlocks(blocks(text, buttons)...), slack.MsgOptionText(plain(text), false)}
	if threadTS != "" {
		options = append(options, slack.MsgOptionTS(threadTS))
	}
	_, ts, err := c.api.PostMessageContext(ctx, channelID, options...)
	return ts, err
}

// UpdateMessage replaces the content of an existing message.
func (c *Client) UpdateMessage(ctx context.Context, channelID, ts, text string, buttons []Button) error {
	_, _, _, err := c.api.UpdateMessageContext(ctx, channelID, ts,
		slack.MsgOptionBlocks(blocks(text, buttons)...), slack.MsgOptionText(plain(text), false))
	return err
}

func blocks(text string, buttons []Button) []slack.Block {
	out := []slack.Block{
		slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, text, false, false), nil, nil),
	}
	if len(buttons) == 0 {
		return out
	}
	elements := make([]slack.BlockElement, 0, len(buttons))
	for _, b := range buttons {
		button := slack.NewButtonBlockElement(b.ActionID, b.Value,
			slack.NewTextBlockObject(slack.PlainTextType, b.Text, true, false))
		switch b.Style {
		case "primary":
			button.Style = slack.StylePrimary
		case "danger":
			button.Style = slack.StyleDanger
		}
		elements = append(elements, button)
	}
	return append(out, slack.NewActionBlock("actions", elements...))
}

// plain is the notification/fallback text for clients that cannot render blocks.
func plain(text string) string {
	if len(text) > 280 {
		return text[:279] + "…"
	}
	return fmt.Sprintf("%s", text)
}
