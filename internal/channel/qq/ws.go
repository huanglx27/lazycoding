package qq

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opReconnect      = 7
	opInvalidSession = 9
	opHello          = 10
	opHeartbeatAck   = 11

	// intentC2C subscribes to private message events (1 << 25).
	intentC2C = 1 << 25
)

type wsPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  int64           `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

// messageEvent carries a received user message.
type messageEvent struct {
	ID      string // QQ message ID (for passive replies)
	OpenID  string // user open_id
	Content string // message text
}

// wsConn manages the WebSocket connection to QQ Gateway.
type wsConn struct {
	token     *TokenManager
	sandbox   bool
	seq       atomic.Int64
	sessionID string
	conn      *websocket.Conn
	onMessage func(messageEvent)
}

func newWSConn(token *TokenManager, sandbox bool) *wsConn {
	return &wsConn{token: token, sandbox: sandbox}
}

func (w *wsConn) gatewayURL() string {
	if w.sandbox {
		return "wss://sandbox.api.sgroup.qq.com/websocket"
	}
	return "wss://api.sgroup.qq.com/websocket"
}

// connect establishes a WebSocket connection and blocks until disconnected.
func (w *wsConn) connect() error {
	conn, _, err := websocket.DefaultDialer.Dial(w.gatewayURL(), nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	w.conn = conn

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("ws read: %w", err)
		}
		if err := w.handle(raw); err != nil {
			return err
		}
	}
}

// start connects with automatic reconnection; blocks indefinitely.
func (w *wsConn) start() {
	for {
		err := w.connect()
		slog.Warn("qq websocket disconnected, reconnecting", "err", err)
		time.Sleep(5 * time.Second)
	}
}

func (w *wsConn) handle(raw []byte) error {
	var p wsPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil // ignore parse failures
	}

	switch p.Op {
	case opHello:
		var d struct {
			HeartbeatInterval int `json:"heartbeat_interval"`
		}
		json.Unmarshal(p.D, &d) //nolint:errcheck
		go w.heartbeat(time.Duration(d.HeartbeatInterval) * time.Millisecond)
		w.identify()

	case opDispatch:
		w.seq.Store(p.S)
		if p.T == "C2C_MESSAGE_CREATE" {
			w.handleC2C(p.D)
		}
		if p.T == "READY" {
			var d struct {
				SessionID string `json:"session_id"`
			}
			json.Unmarshal(p.D, &d) //nolint:errcheck
			w.sessionID = d.SessionID
		}

	case opReconnect:
		return fmt.Errorf("server requested reconnect")

	case opInvalidSession:
		w.sessionID = ""
		time.Sleep(2 * time.Second)
		w.identify()
	}
	return nil
}

func (w *wsConn) identify() {
	type identifyData struct {
		Token   string `json:"token"`
		Intents int    `json:"intents"`
		Shard   [2]int `json:"shard"`
	}
	p := map[string]interface{}{
		"op": opIdentify,
		"d": identifyData{
			Token:   "QQBot " + w.token.Get(),
			Intents: intentC2C,
			Shard:   [2]int{0, 1},
		},
	}
	w.conn.WriteJSON(p) //nolint:errcheck
}

func (w *wsConn) heartbeat(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		seq := w.seq.Load()
		w.conn.WriteJSON(map[string]interface{}{ //nolint:errcheck
			"op": opHeartbeat,
			"d":  seq,
		})
	}
}

func (w *wsConn) handleC2C(raw json.RawMessage) {
	var d struct {
		ID      string `json:"id"`
		Content string `json:"content"`
		Author  struct {
			UserOpenID string `json:"user_openid"`
		} `json:"author"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return
	}
	if w.onMessage != nil {
		w.onMessage(messageEvent{
			ID:      d.ID,
			OpenID:  d.Author.UserOpenID,
			Content: d.Content,
		})
	}
}
