package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Graph is the Mailer through Microsoft Graph: an app registration with the
// Mail.Send application permission sends as From. Modern authentication,
// which Microsoft 365 security defaults allow where they block SMTP
// (plans/mail-via-graph.md). Restrict the app to the one mailbox with an
// Exchange application access policy; Mail.Send alone is tenant-wide.
type Graph struct {
	TenantID     string
	ClientID     string
	ClientSecret string
	From         string // the mailbox to send as, e.g. vrsky@example.com

	// HTTPClient defaults to one with a 30 s timeout. TokenURL and GraphURL
	// default to Microsoft's; tests point them at a local server.
	HTTPClient *http.Client
	TokenURL   string
	GraphURL   string
	now        func() time.Time

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// Send posts the mail to /users/<From>/sendMail. 202 Accepted is success.
func (g *Graph) Send(ctx context.Context, to, subject, body string) error {
	if g.TenantID == "" || g.ClientID == "" || g.ClientSecret == "" || g.From == "" {
		return fmt.Errorf("graph: not configured (set GRAPH_TENANT_ID, GRAPH_CLIENT_ID, GRAPH_CLIENT_SECRET and MAIL_FROM)")
	}
	if to == "" {
		return fmt.Errorf("graph: recipient is empty")
	}
	tok, err := g.accessToken(ctx)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{
		"message": map[string]any{
			"subject":      subject,
			"body":         map[string]string{"contentType": "Text", "content": body},
			"toRecipients": []map[string]any{{"emailAddress": map[string]string{"address": to}}},
		},
		"saveToSentItems": false,
	})
	endpoint := g.graphURL() + "/users/" + url.PathEscape(g.From) + "/sendMail"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client().Do(req)
	if err != nil {
		return fmt.Errorf("graph: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("graph: send as %s: %s", g.From, graphError(resp))
	}
	return nil
}

// accessToken returns a cached client-credentials token, fetching a new one
// a minute before the old one expires.
func (g *Graph) accessToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()
	if g.token != "" && now.Before(g.expiry.Add(-time.Minute)) {
		return g.token, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {g.ClientID},
		"client_secret": {g.ClientSecret},
		"scope":         {"https://graph.microsoft.com/.default"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("graph: token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("graph: token for client %s: %s", g.ClientID, graphError(resp))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil || tr.AccessToken == "" {
		return "", fmt.Errorf("graph: token response not understood (%v)", err)
	}
	g.token = tr.AccessToken
	g.expiry = now.Add(time.Duration(tr.ExpiresIn) * time.Second)
	return g.token, nil
}

// graphError renders an error response as "<status>: <code> <message>",
// from either Graph's {"error":{code,message}} or the token endpoint's
// {"error","error_description"}. Never anything from the request.
func graphError(resp *http.Response) string {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct {
		Error any    `json:"error"`
		Desc  string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &e)
	switch v := e.Error.(type) {
	case map[string]any:
		return fmt.Sprintf("%d: %v %v", resp.StatusCode, v["code"], v["message"])
	case string:
		return fmt.Sprintf("%d: %s %s", resp.StatusCode, v, firstLine(e.Desc))
	}
	return fmt.Sprintf("%d: %s", resp.StatusCode, firstLine(string(raw)))
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return strings.TrimSpace(s)
}

func (g *Graph) client() *http.Client {
	if g.HTTPClient != nil {
		return g.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (g *Graph) tokenURL() string {
	if g.TokenURL != "" {
		return g.TokenURL
	}
	return "https://login.microsoftonline.com/" + url.PathEscape(g.TenantID) + "/oauth2/v2.0/token"
}

func (g *Graph) graphURL() string {
	if g.GraphURL != "" {
		return strings.TrimSuffix(g.GraphURL, "/")
	}
	return "https://graph.microsoft.com/v1.0"
}

func (g *Graph) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}
