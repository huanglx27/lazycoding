// Package qq implements the channel.Channel interface for QQ Bot (private messages).
// It uses QQ's WebSocket Stream Mode to receive C2C_MESSAGE_CREATE events and
// the REST API to reply with segmented messages.
package qq

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/bishenghua/lazycoding/internal/channel"
	"github.com/bishenghua/lazycoding/internal/config"
)

// Adapter implements channel.Channel for QQ Bot private messages.
type Adapter struct {
	cfg    config.QQConfig
	token  *TokenManager
	client *apiClient
	ws     *wsConn
	events chan channel.InboundEvent

	// msgIDCache stores the latest triggering msgID per openid for passive replies.
	// The ConversationID passed around is just openid (stable), while msgID is
	// tracked separately so session keys remain stable across messages.
	msgIDCache sync.Map // map[openid string] → msgID string
}

// New creates a QQ Adapter from config.
func New(cfg *config.Config) *Adapter {
	token := newTokenManager(cfg.QQ.AppID, cfg.QQ.AppSecret)
	client := newAPIClient(token, cfg.QQ.Sandbox)
	ws := newWSConn(token, cfg.QQ.Sandbox)

	a := &Adapter{
		cfg:    cfg.QQ,
		token:  token,
		client: client,
		ws:     ws,
		events: make(chan channel.InboundEvent, 32),
	}
	ws.onMessage = a.onMessage
	return a
}

// Name returns "qq".
func (a *Adapter) Name() string { return "qq" }

// Events starts the QQ WebSocket connection and returns the inbound event stream.
// The connection is held open (with auto-reconnect) until ctx is cancelled.
func (a *Adapter) Events(ctx context.Context) <-chan channel.InboundEvent {
	go func() {
		defer close(a.events)

		if err := a.token.Refresh(); err != nil {
			slog.Error("qq: initial token refresh failed", "err", err)
			return
		}
		go a.token.autoRefresh()

		// ws.start() blocks and auto-reconnects; stop when ctx is done.
		done := make(chan struct{})
		go func() {
			a.ws.start()
			close(done)
		}()

		select {
		case <-ctx.Done():
		case <-done:
		}
	}()

	return a.events
}

// onMessage is called by wsConn whenever a C2C_MESSAGE_CREATE event arrives.
func (a *Adapter) onMessage(e messageEvent) {
	content := strings.TrimSpace(e.Content)
	if content == "" {
		return
	}

	// Allowlist check.
	if !a.isAllowed(e.OpenID) {
		slog.Warn("qq: unauthorized user", "openid", e.OpenID)
		return
	}

	// Store the latest msgID for this user so passive replies work.
	a.msgIDCache.Store(e.OpenID, e.ID)

	ev := channel.InboundEvent{
		UserKey:        "qq:" + e.OpenID,
		ConversationID: e.OpenID, // stable openid; session key is consistent across messages
		Text:           content,
	}

	// Detect slash commands.
	if strings.HasPrefix(content, "/") {
		parts := strings.SplitN(content, " ", 2)
		cmd := strings.TrimPrefix(parts[0], "/")
		ev.IsCommand = true
		ev.Command = cmd
		if len(parts) > 1 {
			ev.CommandArgs = parts[1]
			ev.Text = parts[1]
		} else {
			ev.Text = ""
		}
	}

	select {
	case a.events <- ev:
	default:
		slog.Warn("qq: event queue full, dropping message", "openid", e.OpenID)
		a.client.sendC2CMessage(e.OpenID, "⏳ 队列已满，请稍后重试", e.ID) //nolint:errcheck
	}
}

// isAllowed returns true when the openid is on the allowlist (or no list is set).
func (a *Adapter) isAllowed(openid string) bool {
	if len(a.cfg.AllowFrom) == 0 {
		return true
	}
	for _, id := range a.cfg.AllowFrom {
		if id == openid {
			return true
		}
	}
	return false
}

// latestMsgID returns the last-seen triggering message ID for this openid.
func (a *Adapter) latestMsgID(openid string) string {
	if v, ok := a.msgIDCache.Load(openid); ok {
		return v.(string)
	}
	return ""
}

// SendText sends a plain text message to the user identified by conversationID (openid).
// QQ doesn't support message editing, so the response is sent as a new message.
func (a *Adapter) SendText(ctx context.Context, conversationID string, text string) (channel.MessageHandle, error) {
	text = stripMarkdown(text)
	msgID := a.latestMsgID(conversationID)
	if err := a.client.sendC2CMessage(conversationID, text, msgID); err != nil {
		return nil, fmt.Errorf("qq SendText: %w", err)
	}
	return &qqHandle{}, nil
}

// UpdateText buffers the text inside the handle. The actual message is sent
// when Seal() is called (lazycoding calls UpdateText repeatedly during streaming,
// then Seal() once at the end). For QQ, we send only the final result.
func (a *Adapter) UpdateText(ctx context.Context, handle channel.MessageHandle, text string) error {
	if h, ok := handle.(*qqHandle); ok {
		h.mu.Lock()
		h.latestText = stripMarkdown(text)
		h.mu.Unlock()
	}
	return nil
}

// SendTyping is a no-op for QQ (no typing indicator API).
func (a *Adapter) SendTyping(ctx context.Context, conversationID string) error {
	return nil
}

// SendDocument is not supported by QQ Bot API for private messages; sends a
// text notification instead.
func (a *Adapter) SendDocument(ctx context.Context, conversationID string, filePath string, caption string) error {
	note := fmt.Sprintf("[文件已保存: %s]", filePath)
	if caption != "" {
		note += "\n" + caption
	}
	msgID := a.latestMsgID(conversationID)
	return a.client.sendC2CMessage(conversationID, note, msgID)
}

// SendKeyboard returns a qqHandle that buffers text. QQ has no inline keyboard
// so buttons are ignored. The "(thinking...)" placeholder is NOT sent to QQ
// to avoid orphaned messages; the real response is sent when Seal() fires.
func (a *Adapter) SendKeyboard(ctx context.Context, conversationID string, text string, buttons [][]channel.KeyboardButton) (channel.MessageHandle, error) {
	return &qqHandle{
		client:  a.client,
		openid:  conversationID,
		adapter: a,
	}, nil
}

// AnswerCallback is a no-op for QQ.
func (a *Adapter) AnswerCallback(ctx context.Context, callbackID string, notification string) error {
	return nil
}

// stripMarkdown removes common Markdown and HTML symbols that QQ doesn't render.
func stripMarkdown(s string) string {
	r := strings.NewReplacer(
		"**", "", "__", "", "~~", "",
		"```", "", "`", "",
		"<b>", "", "</b>", "",
		"<i>", "", "</i>", "",
		"<code>", "", "</code>", "",
		"<pre>", "", "</pre>", "",
		"<br>", "\n",
	)
	return r.Replace(s)
}

// qqHandle is the QQ implementation of channel.MessageHandle.
// It buffers the latest text from UpdateText calls and flushes it on Seal().
type qqHandle struct {
	client  *apiClient
	openid  string
	adapter *Adapter
	mu      sync.Mutex
	latestText string
	sealed  bool
}

// Seal sends the buffered text (if any) as the final response to the user.
// lazycoding calls Seal() once when streaming is complete.
func (h *qqHandle) Seal() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sealed || h.latestText == "" {
		h.sealed = true
		return
	}
	h.sealed = true

	msgID := ""
	if h.adapter != nil {
		msgID = h.adapter.latestMsgID(h.openid)
	}
	h.client.sendC2CMessage(h.openid, h.latestText, msgID) //nolint:errcheck
}
