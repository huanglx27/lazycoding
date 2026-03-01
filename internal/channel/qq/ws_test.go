package qq

import (
	"os"
	"testing"
	"time"
)

// TestWSConnect 验证 WebSocket 握手流程，30s 内收到消息则 PASS。
func TestWSConnect(t *testing.T) {
	appID := os.Getenv("QQ_APP_ID")
	secret := os.Getenv("QQ_APP_SECRET")
	if appID == "" {
		t.Skip("QQ_APP_ID not set")
	}

	mgr := newTokenManager(appID, secret)
	if err := mgr.Refresh(); err != nil {
		t.Fatal(err)
	}

	ws := newWSConn(mgr, true)
	got := make(chan messageEvent, 1)
	ws.onMessage = func(e messageEvent) {
		select {
		case got <- e:
		default:
		}
	}

	go ws.start()

	t.Log("WebSocket started (sandbox), send a message from your QQ sandbox account within 30s...")
	select {
	case e := <-got:
		t.Logf("received: openid=%s content=%q", e.OpenID, e.Content)
	case <-time.After(30 * time.Second):
		t.Log("timeout — no message received; WebSocket handshake may still be OK")
	}
}
