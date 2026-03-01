package qq

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
)

// apiClient sends messages via the QQ Bot REST API.
type apiClient struct {
	token   *TokenManager
	sandbox bool
	seq     atomic.Uint64
}

func newAPIClient(token *TokenManager, sandbox bool) *apiClient {
	return &apiClient{token: token, sandbox: sandbox}
}

func (c *apiClient) baseURL() string {
	if c.sandbox {
		return "https://sandbox.api.sgroup.qq.com"
	}
	return "https://api.sgroup.qq.com"
}

// sendC2CMessage sends a private text message to openid.
// msgID is the triggering message ID for passive replies (may be empty for active messages).
func (c *apiClient) sendC2CMessage(openid, content, msgID string) error {
	url := fmt.Sprintf("%s/v2/users/%s/messages", c.baseURL(), openid)

	payload := map[string]interface{}{
		"content":  content,
		"msg_type": 0, // text
		"msg_seq":  c.seq.Add(1),
	}
	if msgID != "" {
		payload["msg_id"] = msgID
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "QQBot "+c.token.Get())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var errBody map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errBody) //nolint:errcheck
		return fmt.Errorf("send message HTTP %d: %v", resp.StatusCode, errBody)
	}
	return nil
}
