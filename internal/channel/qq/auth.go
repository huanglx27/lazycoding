package qq

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const tokenURL = "https://bots.qq.com/app/getAppAccessToken"

// TokenManager handles QQ Bot access token lifecycle.
type TokenManager struct {
	appID     string
	appSecret string
	mu        sync.RWMutex
	token     string
	expiresAt time.Time
}

func newTokenManager(appID, appSecret string) *TokenManager {
	return &TokenManager{appID: appID, appSecret: appSecret}
}

// Get returns the current valid token (thread-safe).
func (m *TokenManager) Get() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.token
}

// Refresh fetches a new token immediately.
func (m *TokenManager) Refresh() error {
	body := fmt.Sprintf(`{"appId":%q,"clientSecret":%q}`, m.appID, m.appSecret)
	resp, err := http.Post(tokenURL, "application/json", strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   string `json:"expires_in"` // API returns string, e.g., "7200"
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("token decode: %w", err)
	}
	if result.AccessToken == "" {
		return fmt.Errorf("empty access_token in response")
	}

	// Convert string ExpiresIn to an integer
	var exp int
	fmt.Sscanf(result.ExpiresIn, "%d", &exp)

	m.mu.Lock()
	m.token = result.AccessToken
	m.expiresAt = time.Now().Add(time.Duration(exp) * time.Second)
	m.mu.Unlock()
	return nil
}

// autoRefresh refreshes the token 60s before expiry; run in a goroutine.
func (m *TokenManager) autoRefresh() {
	for {
		m.mu.RLock()
		remaining := time.Until(m.expiresAt)
		m.mu.RUnlock()

		sleepDur := remaining - 60*time.Second
		if sleepDur < 0 {
			sleepDur = 0
		}
		time.Sleep(sleepDur)

		if err := m.Refresh(); err != nil {
			time.Sleep(10 * time.Second)
		}
	}
}
