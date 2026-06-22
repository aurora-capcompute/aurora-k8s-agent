package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

type sanitizedError struct {
	message string
	cause   error
}

func (e *sanitizedError) Error() string { return e.message }
func (e *sanitizedError) Is(target error) bool {
	return errors.Is(e.cause, target)
}

func NewClient(token string) *Client {
	return &Client{
		baseURL: "https://api.telegram.org",
		token:   token,
		http:    &http.Client{Timeout: 40 * time.Second},
	}
}

func (c *Client) SetBaseURL(value string) { c.baseURL = strings.TrimRight(value, "/") }

func (c *Client) SetHTTPClient(value *http.Client) {
	if value != nil {
		c.http = value
	}
}

func (c *Client) GetMe(ctx context.Context) (BotIdentity, error) {
	var identity BotIdentity
	err := c.call(ctx, "getMe", nil, &identity)
	return identity, err
}

func (c *Client) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	var updates []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": 30,
		"allowed_updates": []string{"message", "callback_query"},
	}, &updates)
	return updates, err
}

func (c *Client) SendMessage(
	ctx context.Context,
	chatID int64,
	text string,
	keyboard *InlineKeyboardMarkup,
) (Message, error) {
	payload := map[string]any{
		"chat_id": chatID, "text": text,
		"parse_mode": "HTML", "disable_web_page_preview": true,
	}
	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}
	var message Message
	err := c.call(ctx, "sendMessage", payload, &message)
	return message, err
}

func (c *Client) EditMessage(
	ctx context.Context,
	chatID, messageID int64,
	text string,
	keyboard *InlineKeyboardMarkup,
) error {
	payload := map[string]any{
		"chat_id": chatID, "message_id": messageID, "text": text,
		"parse_mode": "HTML", "disable_web_page_preview": true,
	}
	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}
	return c.call(ctx, "editMessageText", payload, nil)
}

func (c *Client) EditKeyboard(
	ctx context.Context,
	chatID, messageID int64,
	keyboard *InlineKeyboardMarkup,
) error {
	return c.call(ctx, "editMessageReplyMarkup", map[string]any{
		"chat_id": chatID, "message_id": messageID, "reply_markup": keyboard,
	}, nil)
}

func (c *Client) AnswerCallback(ctx context.Context, id, text string, alert bool) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": id, "text": text, "show_alert": alert,
	}, nil)
}

func (c *Client) call(ctx context.Context, method string, payload any, target any) (resultErr error) {
	defer func() {
		resultErr = c.sanitizeError(resultErr)
	}()

	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	endpoint, err := url.JoinPath(c.baseURL, "bot"+c.token, method)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("telegram HTTP %s: %w", response.Status, err)
	}
	if !envelope.OK {
		if envelope.Description == "" {
			envelope.Description = "Telegram API request failed"
		}
		return errors.New(strconv.Itoa(envelope.ErrorCode) + ": " + envelope.Description)
	}
	if target == nil || len(envelope.Result) == 0 {
		return nil
	}
	return json.Unmarshal(envelope.Result, target)
}

func (c *Client) sanitizeError(err error) error {
	if err == nil || c.token == "" {
		return err
	}
	message := strings.ReplaceAll(err.Error(), c.token, "[REDACTED]")
	if message == err.Error() {
		return err
	}
	return &sanitizedError{message: message, cause: err}
}
