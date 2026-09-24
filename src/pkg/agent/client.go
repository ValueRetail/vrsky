package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
)

// Version is the agent's version, set at build time with
// -ldflags "-X github.com/ValueRetail/vrsky/pkg/agent.Version=…".
var Version = "dev"

// APIError is a non-2xx answer from the gateway.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("VRSky answered %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("VRSky answered %d", e.Status)
}

// IsRevoked reports whether VRSky refused the credential as revoked.
func IsRevoked(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == agentproto.ErrAgentRevoked
}

// Client speaks the agent protocol to one gateway.
type Client struct {
	base       *url.URL
	credential string
	http       *http.Client
}

// NewClient returns a client for serverURL. credential may be empty for
// registration.
func NewClient(serverURL, credential string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(serverURL, "/"))
	if err != nil {
		return nil, err
	}
	return &Client{
		base:       u,
		credential: credential,
		http: &http.Client{
			// No overall timeout: bodies can be large. Each call bounds
			// itself with a context; these bound the connection itself.
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: agentproto.PollHoldMax + 30*time.Second,
				IdleConnTimeout:       90 * time.Second,
				MaxIdleConnsPerHost:   4,
			},
		},
	}, nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, q url.Values, body io.Reader) (*http.Request, error) {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	if q != nil {
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set(agentproto.HeaderProto, strconv.Itoa(agentproto.ProtoVersion))
	req.Header.Set("User-Agent", "vrsky-agent/"+Version+" ("+runtime.GOOS+"/"+runtime.GOARCH+")")
	if c.credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.credential)
	}
	return req, nil
}

// do sends a request and decodes a JSON answer into out (if non-nil).
func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return readAPIError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func readAPIError(resp *http.Response) error {
	ae := &APIError{Status: resp.StatusCode}
	var er agentproto.ErrorResponse
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(body, &er) == nil && er.Error != "" {
		ae.Code, ae.Message = er.Error, er.Message
	} else {
		ae.Message = strings.TrimSpace(string(body))
		if len(ae.Message) > 200 {
			ae.Message = ae.Message[:200]
		}
	}
	return ae
}

func jsonBody(v any) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

// Register exchanges a one-time token for a credential.
func (c *Client) Register(ctx context.Context, req agentproto.RegisterRequest) (*agentproto.RegisterResponse, error) {
	r, err := c.newRequest(ctx, http.MethodPost, "/agent/v1/register", nil, jsonBody(req))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	var out agentproto.RegisterResponse
	if err := c.do(r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Announce reports machine details and folder names.
func (c *Client) Announce(ctx context.Context, req agentproto.AnnounceRequest) error {
	r, err := c.newRequest(ctx, http.MethodPost, "/agent/v1/announce", nil, jsonBody(req))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	return c.do(r, nil)
}

// Work long-polls for work. waitSeconds bounds the hold; watchesVersion is the
// version from the previous answer ("" on the first poll).
func (c *Client) Work(ctx context.Context, waitSeconds int, watchesVersion string) (*agentproto.WorkResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(waitSeconds)*time.Second+30*time.Second)
	defer cancel()
	q := url.Values{"wait": {strconv.Itoa(waitSeconds)}, "watches": {watchesVersion}}
	r, err := c.newRequest(ctx, http.MethodGet, "/agent/v1/work", q, nil)
	if err != nil {
		return nil, err
	}
	var out agentproto.WorkResponse
	if err := c.do(r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Body opens a delivery's payload stream. The caller closes it.
func (c *Client) Body(ctx context.Context, bodyURL string) (io.ReadCloser, error) {
	// The gateway sends a path; only paths under /agent/v1/ are followed,
	// so a delivery can never point the agent (and its credential) elsewhere.
	if !strings.HasPrefix(bodyURL, "/agent/v1/deliveries/") || strings.Contains(bodyURL, "..") {
		return nil, fmt.Errorf("refusing body URL %q", bodyURL)
	}
	r, err := c.newRequest(ctx, http.MethodGet, bodyURL, nil, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, readAPIError(resp)
	}
	return resp.Body, nil
}

// Ack reports a delivery's outcome.
func (c *Client) Ack(ctx context.Context, deliveryID string, writeErr error) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req := agentproto.AckRequest{Status: agentproto.AckOK}
	if writeErr != nil {
		req = agentproto.AckRequest{Status: agentproto.AckFailed, Error: writeErr.Error()}
	}
	r, err := c.newRequest(ctx, http.MethodPost, "/agent/v1/deliveries/"+url.PathEscape(deliveryID)+"/ack", nil, jsonBody(req))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	return c.do(r, nil)
}

// Upload streams one file into a pipeline. uploadID must be stable across
// retries of the same file, so VRSky can drop a duplicate.
func (c *Client) Upload(ctx context.Context, connectionID, directory, filename, uploadID string, body io.Reader, size int64) error {
	q := url.Values{
		"connection_id": {connectionID}, "directory": {directory},
		"filename": {filename}, "upload_id": {uploadID},
	}
	r, err := c.newRequest(ctx, http.MethodPost, "/agent/v1/uploads", q, body)
	if err != nil {
		return err
	}
	r.ContentLength = size
	r.Header.Set("Content-Type", "application/octet-stream")
	return c.do(r, nil)
}
