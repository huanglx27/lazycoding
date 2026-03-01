package qq

import (
	"os"
	"testing"
)

func TestSendC2CMessage(t *testing.T) {
	appID := os.Getenv("QQ_APP_ID")
	secret := os.Getenv("QQ_APP_SECRET")
	openid := os.Getenv("QQ_TEST_OPENID") // 沙箱测试用户的 openid
	if appID == "" || openid == "" {
		t.Skip("QQ credentials not set")
	}

	mgr := newTokenManager(appID, secret)
	if err := mgr.Refresh(); err != nil {
		t.Fatal(err)
	}

	client := newAPIClient(mgr, true) // sandbox=true
	err := client.sendC2CMessage(openid, "hello from test", "")
	if err != nil {
		t.Fatalf("sendC2CMessage: %v", err)
	}
	t.Log("message sent, check QQ sandbox")
}
