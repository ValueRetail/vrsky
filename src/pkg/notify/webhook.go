package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Webhook POSTs the alert as JSON to an arbitrary URL. When Secret is set the
// request carries X-VRSky-Signature: hex(HMAC-SHA256(body)) so the receiver can
// verify authenticity — the same scheme VRSky's own webhook-consumer verifies.
type Webhook struct {
	URL    string
	Secret string       // optional HMAC key (a secret, resolved by the caller)
	Client *http.Client // nil -> shared default
}

func (w *Webhook) Send(ctx context.Context, a *Alert) error {
	if w.URL == "" {
		return fmt.Errorf("webhook: URL is empty")
	}
	body, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("webhook: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if w.Secret != "" {
		mac := hmac.New(sha256.New, []byte(w.Secret))
		mac.Write(body)
		req.Header.Set("X-VRSky-Signature", hex.EncodeToString(mac.Sum(nil)))
	}

	client := w.Client
	if client == nil {
		client = httpClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("webhook: %s returned %d: %s", w.URL, resp.StatusCode, b)
	}
	return nil
}
