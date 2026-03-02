package channel

import (
	"context"
	"fmt"
	"sync"
)

// KeyboardButton is one button in an inline keyboard row.
type KeyboardButton struct {
	Text string // label shown to the user
	Data string // opaque callback data sent back when pressed
}

// InboundEvent represents a message, command, or inline keyboard button press
// arriving from a chat platform.
type InboundEvent struct {
	UserKey        string // platform-scoped user identifier, e.g. "tg:123456"
	ConversationID string // chat/channel identifier passed back to Send* methods
	Text           string // message text (for voice: the transcription)
	IsCommand      bool
	Command        string // without the leading slash, e.g. "reset"
	CommandArgs    string // text after the command

	// IsVoice is true when the text was transcribed from a voice message.
	IsVoice bool

	// IsCallback is true when the event originates from an inline keyboard button press.
	IsCallback   bool
	CallbackID   string // opaque ID used with AnswerCallback
	CallbackData string // data string attached to the pressed button
}

// MessageHandle is an opaque reference to a sent message that can be edited.
// Seal must be called when no further edits will be made.
type MessageHandle interface {
	Seal()
}

// Channel abstracts the chat platform (Telegram, Slack, …).
type Channel interface {
	// Events returns a channel that emits inbound events until ctx is cancelled.
	Events(ctx context.Context) <-chan InboundEvent

	// SendText sends a new message and returns an editable handle.
	SendText(ctx context.Context, conversationID string, text string) (MessageHandle, error)

	// UpdateText replaces the content of a previously sent message.
	// A no-op if the handle has been Seal()ed.
	UpdateText(ctx context.Context, handle MessageHandle, text string) error

	// SendTyping sends a transient "typing…" indicator.
	SendTyping(ctx context.Context, conversationID string) error

	// SendDocument uploads a local file to the conversation.
	// caption may be empty.
	SendDocument(ctx context.Context, conversationID string, filePath string, caption string) error

	// SendKeyboard sends a message with an inline keyboard.
	// buttons is a 2-D slice: outer index = row, inner index = button in that row.
	SendKeyboard(ctx context.Context, conversationID string, text string, buttons [][]KeyboardButton) (MessageHandle, error)

	// AnswerCallback acknowledges an inline keyboard button press so Telegram
	// removes the loading spinner. notification is shown briefly (may be empty).
	AnswerCallback(ctx context.Context, callbackID string, notification string) error
}

// ── MultiAdapter ──────────────────────────────────────────────────────────────

// NewMultiAdapter returns a Channel that fans in events from all provided
// adapters and routes outbound calls back to the originating adapter.
// If only one adapter is provided it is returned directly (no wrapping).
func NewMultiAdapter(adapters ...Channel) Channel {
	if len(adapters) == 1 {
		return adapters[0]
	}
	return &multiAdapter{
		adapters: adapters,
		events:   make(chan InboundEvent, 64),
		routes:   make(map[string]Channel),
	}
}

type multiAdapter struct {
	adapters []Channel
	events   chan InboundEvent

	routeMu sync.RWMutex
	routes  map[string]Channel // conversationID → owning adapter
}

func (m *multiAdapter) Events(ctx context.Context) <-chan InboundEvent {
	var wg sync.WaitGroup
	for _, a := range m.adapters {
		wg.Add(1)
		a := a
		go func() {
			defer wg.Done()
			for ev := range a.Events(ctx) {
				m.routeMu.Lock()
				m.routes[ev.ConversationID] = a
				m.routeMu.Unlock()
				select {
				case m.events <- ev:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(m.events)
	}()
	return m.events
}

func (m *multiAdapter) adapterFor(convID string) Channel {
	m.routeMu.RLock()
	defer m.routeMu.RUnlock()
	return m.routes[convID]
}

func (m *multiAdapter) SendText(ctx context.Context, conversationID, text string) (MessageHandle, error) {
	a := m.adapterFor(conversationID)
	if a == nil {
		return nil, fmt.Errorf("multi: no adapter known for conversation %s", conversationID)
	}
	h, err := a.SendText(ctx, conversationID, text)
	if err != nil {
		return nil, err
	}
	return &multiHandle{inner: h, adapter: a}, nil
}

func (m *multiAdapter) UpdateText(ctx context.Context, handle MessageHandle, text string) error {
	mh, ok := handle.(*multiHandle)
	if !ok {
		return fmt.Errorf("multi: unexpected handle type %T", handle)
	}
	return mh.adapter.UpdateText(ctx, mh.inner, text)
}

func (m *multiAdapter) SendTyping(ctx context.Context, conversationID string) error {
	if a := m.adapterFor(conversationID); a != nil {
		return a.SendTyping(ctx, conversationID)
	}
	return nil
}

func (m *multiAdapter) SendKeyboard(ctx context.Context, conversationID, text string, buttons [][]KeyboardButton) (MessageHandle, error) {
	a := m.adapterFor(conversationID)
	if a == nil {
		return nil, fmt.Errorf("multi: no adapter known for conversation %s", conversationID)
	}
	h, err := a.SendKeyboard(ctx, conversationID, text, buttons)
	if err != nil {
		return nil, err
	}
	return &multiHandle{inner: h, adapter: a}, nil
}

// AnswerCallback tries all adapters. Feishu's is always a no-op; Telegram's
// errors are ignored at the call site, so there's no cost to trying both.
func (m *multiAdapter) AnswerCallback(ctx context.Context, callbackID, notification string) error {
	var lastErr error
	for _, a := range m.adapters {
		if err := a.AnswerCallback(ctx, callbackID, notification); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (m *multiAdapter) SendDocument(ctx context.Context, conversationID, filePath, caption string) error {
	a := m.adapterFor(conversationID)
	if a == nil {
		return fmt.Errorf("multi: no adapter known for conversation %s", conversationID)
	}
	return a.SendDocument(ctx, conversationID, filePath, caption)
}

// multiHandle wraps an adapter-specific MessageHandle, preserving the
// originating adapter so UpdateText can route back to the correct one.
type multiHandle struct {
	inner   MessageHandle
	adapter Channel
}

func (h *multiHandle) Seal() { h.inner.Seal() }
