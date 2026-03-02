package feishu

// WebSocket long-connection mode for Feishu.
//
// Instead of exposing an HTTP server (which requires a public IP), the adapter
// opens an outbound WebSocket connection to Feishu's event-push endpoint.
// This is equivalent to Telegram's long-polling model: the bot initiates the
// connection, so it works behind NAT/firewall with no port-forwarding needed.
//
// Wire protocol: binary frames encoded as protobuf.
// Schema (from github.com/larksuite/oapi-sdk-go ws/pbbp2.pb.go):
//
//	Header { key: string(1), value: string(2) }
//	Frame  { seq_id: uint64(1), log_id: uint64(2), service: int32(3),
//	          method: int32(4), headers: repeated Header(5),
//	          payload_encoding: string(6), payload_type: string(7),
//	          payload: bytes(8), log_id_new: string(9) }
//
// method: 0 = control (ping/pong), 1 = data (events)

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

const wsEndpointAPI = "https://open.feishu.cn/callback/ws/endpoint"

const (
	wsMsgTypeControl = int32(0) // ping / pong
	wsMsgTypeData    = int32(1) // events
)

type wsClientConfig struct {
	ReconnectCount    int `json:"ReconnectCount"`
	ReconnectInterval int `json:"ReconnectInterval"`
	ReconnectNonce    int `json:"ReconnectNonce"`
	PingInterval      int `json:"PingInterval"`
}

// ── Minimal protobuf encoder ──────────────────────────────────────────────────

func pbVarint(v uint64) []byte {
	var b [10]byte
	n := 0
	for v >= 0x80 {
		b[n] = byte(v) | 0x80
		v >>= 7
		n++
	}
	b[n] = byte(v)
	return b[:n+1]
}

func pbTag(field, wt int) []byte { return pbVarint(uint64(field<<3 | wt)) }

func pbUint64Field(b []byte, field int, v uint64) []byte {
	if v == 0 {
		return b
	}
	return append(append(b, pbTag(field, 0)...), pbVarint(v)...)
}

func pbInt32Field(b []byte, field int, v int32) []byte {
	if v == 0 {
		return b
	}
	return append(append(b, pbTag(field, 0)...), pbVarint(uint64(v))...)
}

func pbBytesField(b []byte, field int, data []byte) []byte {
	b = append(b, pbTag(field, 2)...)
	b = append(b, pbVarint(uint64(len(data)))...)
	return append(b, data...)
}

func pbStringField(b []byte, field int, s string) []byte {
	return pbBytesField(b, field, []byte(s))
}

type pbKV struct{ k, v string }

type pbFrame struct {
	seqID   uint64
	service int32
	method  int32
	headers []pbKV
	payload []byte
}

func (f *pbFrame) headerMap() map[string]string {
	m := make(map[string]string, len(f.headers))
	for _, h := range f.headers {
		m[h.k] = h.v
	}
	return m
}

func encodeKV(k, v string) []byte {
	var b []byte
	b = pbStringField(b, 1, k)
	b = pbStringField(b, 2, v)
	return b
}

func encodePBFrame(f pbFrame) []byte {
	var b []byte
	b = pbUint64Field(b, 1, f.seqID)
	b = pbInt32Field(b, 3, f.service)
	b = pbInt32Field(b, 4, f.method)
	for _, h := range f.headers {
		b = pbBytesField(b, 5, encodeKV(h.k, h.v))
	}
	if len(f.payload) > 0 {
		b = pbBytesField(b, 8, f.payload)
	}
	return b
}

// ── Minimal protobuf decoder ──────────────────────────────────────────────────

func pbReadVarint(data []byte) (v uint64, n int) {
	for i, b := range data {
		if i >= 10 {
			return 0, 0
		}
		v |= uint64(b&0x7f) << (7 * uint(i))
		if b < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}

func decodePBFrame(data []byte) (*pbFrame, error) {
	f := &pbFrame{}
	for len(data) > 0 {
		tag, n := pbReadVarint(data)
		if n == 0 {
			return nil, fmt.Errorf("bad frame tag")
		}
		data = data[n:]
		field, wt := int(tag>>3), int(tag&7)
		switch wt {
		case 0: // varint
			v, n2 := pbReadVarint(data)
			if n2 == 0 {
				return nil, fmt.Errorf("bad varint field %d", field)
			}
			data = data[n2:]
			switch field {
			case 1:
				f.seqID = v
			case 3:
				f.service = int32(v)
			case 4:
				f.method = int32(v)
			}
		case 1: // 64-bit fixed — skip
			if len(data) < 8 {
				return nil, fmt.Errorf("truncated 64-bit field %d", field)
			}
			data = data[8:]
		case 2: // length-delimited
			l, n2 := pbReadVarint(data)
			if n2 == 0 || uint64(len(data)-n2) < l {
				return nil, fmt.Errorf("bad length field %d", field)
			}
			val := data[n2 : n2+int(l)]
			data = data[n2+int(l):]
			switch field {
			case 5:
				kv, err := decodeKV(val)
				if err != nil {
					return nil, err
				}
				f.headers = append(f.headers, kv)
			case 8:
				f.payload = append([]byte{}, val...)
			}
		case 5: // 32-bit fixed — skip
			if len(data) < 4 {
				return nil, fmt.Errorf("truncated 32-bit field %d", field)
			}
			data = data[4:]
		default:
			return nil, fmt.Errorf("unsupported wire type %d field %d", wt, field)
		}
	}
	return f, nil
}

func decodeKV(data []byte) (pbKV, error) {
	var kv pbKV
	for len(data) > 0 {
		tag, n := pbReadVarint(data)
		if n == 0 {
			return kv, fmt.Errorf("bad kv tag")
		}
		data = data[n:]
		if int(tag&7) != 2 {
			return kv, fmt.Errorf("non-string wire type %d in kv", tag&7)
		}
		l, n2 := pbReadVarint(data)
		if n2 == 0 || uint64(len(data)-n2) < l {
			return kv, fmt.Errorf("bad kv value length")
		}
		val := string(data[n2 : n2+int(l)])
		data = data[n2+int(l):]
		switch int(tag >> 3) {
		case 1:
			kv.k = val
		case 2:
			kv.v = val
		}
	}
	return kv, nil
}

// ── WebSocket long connection ─────────────────────────────────────────────────

func (a *Adapter) getWSEndpoint(ctx context.Context) (string, *wsClientConfig, error) {
	body, _ := json.Marshal(map[string]string{
		"AppID":     a.cfg.AppID,
		"AppSecret": a.cfg.AppSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wsEndpointAPI, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	raw, err := doHTTP(req)
	if err != nil {
		return "", nil, err
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			URL          string          `json:"URL"`
			ClientConfig *wsClientConfig `json:"ClientConfig"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", nil, fmt.Errorf("decode ws endpoint: %w", err)
	}
	if resp.Code != 0 || resp.Data.URL == "" {
		return "", nil, fmt.Errorf("get ws endpoint: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return resp.Data.URL, resp.Data.ClientConfig, nil
}

// runWebSocket loops forever, reconnecting on failure, until ctx is cancelled.
func (a *Adapter) runWebSocket(ctx context.Context) {
	backoff := 2 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		wsURL, clientCfg, err := a.getWSEndpoint(ctx)
		if err != nil {
			slog.Error("feishu ws: get endpoint", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				backoff = min(backoff*2, 60*time.Second)
			}
			continue
		}
		backoff = 2 * time.Second

		u, _ := url.Parse(wsURL)
		svcID, _ := strconv.ParseInt(u.Query().Get("service_id"), 10, 32)

		conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
		if err != nil {
			slog.Error("feishu ws: dial", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		slog.Info("feishu ws: connected")
		if err := a.serveWSConn(ctx, conn, int32(svcID), clientCfg); err != nil && ctx.Err() == nil {
			slog.Warn("feishu ws: disconnected, reconnecting", "err", err)
		}
		conn.Close()
	}
}

func (a *Adapter) serveWSConn(ctx context.Context, conn *websocket.Conn, serviceID int32, cfg *wsClientConfig) error {
	pingInterval := 120 * time.Second
	if cfg != nil && cfg.PingInterval > 0 {
		pingInterval = time.Duration(cfg.PingInterval) * time.Second
	}

	var seqID uint64
	nextSeq := func() uint64 { seqID++; return seqID }

	// Send initial ping to confirm connection.
	if err := a.wsPing(conn, serviceID, nextSeq()); err != nil {
		return fmt.Errorf("initial ping: %w", err)
	}

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	frameCh := make(chan *pbFrame, 8)
	errCh := make(chan error, 1)

	go func() {
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			if mt != websocket.BinaryMessage {
				continue
			}
			f, err := decodePBFrame(msg)
			if err != nil {
				slog.Debug("feishu ws: decode frame", "err", err)
				continue
			}
			select {
			case frameCh <- f:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			conn.WriteMessage(websocket.CloseMessage, //nolint:errcheck
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return nil
		case err := <-errCh:
			return err
		case <-pingTicker.C:
			if err := a.wsPing(conn, serviceID, nextSeq()); err != nil {
				return fmt.Errorf("ping: %w", err)
			}
		case f := <-frameCh:
			if f.method == wsMsgTypeControl {
				// Pong may carry updated client config from server.
				if len(f.payload) > 0 {
					var newCfg wsClientConfig
					if json.Unmarshal(f.payload, &newCfg) == nil && newCfg.PingInterval > 0 {
						pingTicker.Reset(time.Duration(newCfg.PingInterval) * time.Second)
					}
				}
				continue
			}
			// Data frame: ACK first, then dispatch asynchronously.
			a.wsACK(conn, f, nextSeq()) //nolint:errcheck
			if f.headerMap()["type"] == "event" {
				go a.dispatchRaw(ctx, f.payload)
			}
		}
	}
}

func (a *Adapter) wsPing(conn *websocket.Conn, serviceID int32, seqID uint64) error {
	f := pbFrame{seqID: seqID, service: serviceID, method: wsMsgTypeControl}
	return conn.WriteMessage(websocket.BinaryMessage, encodePBFrame(f))
}

func (a *Adapter) wsACK(conn *websocket.Conn, received *pbFrame, seqID uint64) error {
	ack := pbFrame{
		seqID:   seqID,
		method:  wsMsgTypeData,
		headers: received.headers,
		payload: []byte(`{"code":200}`),
	}
	return conn.WriteMessage(websocket.BinaryMessage, encodePBFrame(ack))
}

// dispatchRaw parses a raw JSON event payload (WebSocket path) and dispatches
// it through the same dedup + dispatch pipeline as webhook events.
func (a *Adapter) dispatchRaw(ctx context.Context, body []byte) {
	var env feishuEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		slog.Debug("feishu ws: unmarshal event", "err", err)
		return
	}
	if env.Challenge != "" {
		return // challenges don't occur in WebSocket mode
	}
	if env.Header.EventID != "" {
		a.seenMu.Lock()
		_, dup := a.seen[env.Header.EventID]
		if !dup {
			a.seen[env.Header.EventID] = time.Now()
		}
		a.seenMu.Unlock()
		if dup {
			return
		}
	}
	a.dispatch(ctx, env)
}
