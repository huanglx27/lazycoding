package qq

import (
	"os"
	"testing"
)

func TestTokenRefresh(t *testing.T) {
	appID := os.Getenv("QQ_APP_ID")
	secret := os.Getenv("QQ_APP_SECRET")
	if appID == "" {
		t.Skip("QQ_APP_ID not set")
	}

	mgr := newTokenManager(appID, secret)
	if err := mgr.Refresh(); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
	tok := mgr.Get()
	if len(tok) < 10 {
		t.Fatalf("token too short: %q", tok)
	}
	t.Logf("token prefix: %s...", tok[:10])
}
