package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// The calls below are the four endpoints of Bifrost's staged import
// (crates/catalogue-api/src/routes.rs, docs/catalogue-doors.md). Every one
// needs "Authorization: Bearer <tenant token>" with permission
// catalogue.import; X-Tenant-Id, when sent, must match the credential's tenant
// or Bifrost answers 403.
const (
	importsPath = "/v1/catalogue/imports"

	// maxResponseBytes caps what is read of any answer. A commit report lists
	// at most the rejected rows, a few hundred bytes each.
	maxResponseBytes = 1 << 20
	// maxReasonRunes caps Bifrost's reason in an error text. The text reaches
	// the activity feed and the DLQ header.
	maxReasonRunes = 300
)

// bifrostClient drives one node's import calls.
type bifrostClient struct {
	http     *http.Client
	logger   *slog.Logger
	baseURL  string // no trailing slash
	token    string
	tenantID string // optional X-Tenant-Id
}

// beginBody is the body of POST /v1/catalogue/imports. effective_from is left
// out, so Bifrost uses the time of begin (Utc::now() in routes.rs) as the
// default; an item may carry its own.
type beginBody struct {
	Kind        string `json:"kind"`
	AllowShrink string `json:"allow_shrink,omitempty"`
}

// commitReport is what commit answers. Fields Bifrost adds later are ignored.
type commitReport struct {
	ImportID      string     `json:"import_id"`
	Version       string     `json:"version"`
	Applied       int        `json:"applied"`
	CatalogueSize int        `json:"catalogue_size"`
	Rejected      int        `json:"rejected"`
	Errors        []rowError `json:"errors"`
}

type rowError struct {
	Row       int    `json:"row"`
	ArticleNo string `json:"article_no"`
	Error     string `json:"error"`
}

// userError is an error whose text is safe to show a tenant while the cause
// stays reachable for errors.Is/As and, through safeurl.Detail, the log.
type userError struct {
	msg   string
	cause error
}

func (e *userError) Error() string { return e.msg }
func (e *userError) Unwrap() error { return e.cause }

// begin opens an import and returns its id.
func (c *bifrostClient) begin(ctx context.Context, kind, allowShrink string) (string, error) {
	body, err := json.Marshal(beginBody{Kind: kind, AllowShrink: allowShrink})
	if err != nil {
		return "", sdk.Permanent(fmt.Errorf("bifrost begin: %w", err))
	}
	status, resp, hdr, err := c.do(ctx, http.MethodPost, importsPath, body, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return "", c.classify("begin", status, resp, hdr)
	}
	var out struct {
		ImportID string `json:"import_id"`
	}
	if err := json.Unmarshal(resp, &out); err != nil || strings.TrimSpace(out.ImportID) == "" {
		// A 2xx without an id is not Bifrost's catalogue-api: a proxy page or
		// a wrong base_url. Nothing was opened that can be named.
		return "", sdk.Retriable(errors.New("bifrost begin: answer has no import_id; check base_url"))
	}
	return out.ImportID, nil
}

// addItems sends one batch. key makes a resend of the same batch inside this
// import a no-op on Bifrost's side (Idempotency-Key, remembered per import).
func (c *bifrostClient) addItems(ctx context.Context, id, key string, items []json.RawMessage, wantTotal int) error {
	body, err := json.Marshal(struct {
		Items []json.RawMessage `json:"items"`
	}{items})
	if err != nil {
		return sdk.Permanent(fmt.Errorf("bifrost items: %w", err))
	}
	status, resp, hdr, err := c.do(ctx, http.MethodPost, importPath(id)+"/items", body, map[string]string{"Idempotency-Key": key})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return c.classify("items", status, resp, hdr)
	}
	// Bifrost counts what it holds. A different number means a batch was
	// lost or doubled on the way (or the answer is not Bifrost's): commit
	// would publish a different list than the one sent.
	var out struct {
		TotalSoFar *int `json:"total_so_far"`
	}
	if err := json.Unmarshal(resp, &out); err != nil || out.TotalSoFar == nil {
		return sdk.Retriable(errors.New("bifrost items: answer has no total_so_far; check base_url and any proxy in front of Bifrost"))
	}
	if *out.TotalSoFar != wantTotal {
		return sdk.Retriable(fmt.Errorf("bifrost items: import holds %d items after this batch, sent %d", *out.TotalSoFar, wantTotal))
	}
	return nil
}

// commit makes the import visible. Bifrost takes the import out of its table
// whatever the outcome, so a failed commit leaves nothing to abort.
func (c *bifrostClient) commit(ctx context.Context, id string) (*commitReport, error) {
	status, resp, hdr, err := c.do(ctx, http.MethodPost, importPath(id)+"/commit", nil, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, c.classify("commit", status, resp, hdr)
	}
	// The commit is done once Bifrost answers 200, so a report that cannot
	// be read is not worth a re-run of an import that landed. But a 200 that
	// is not Bifrost's (a proxy's HTML page) proves nothing, so only a body starting with "{" counts.
	var rep commitReport
	if err := json.Unmarshal(resp, &rep); err != nil {
		if !bytes.HasPrefix(bytes.TrimSpace(resp), []byte("{")) {
			return nil, sdk.Retriable(errors.New("bifrost commit: 200 answer is not a JSON report; check base_url and any proxy in front of Bifrost"))
		}
		if len(resp) > maxResponseBytes {
			c.logger.Warn("bifrost commit report is longer than the read limit; the rejected count is unknown", "import_id", id)
		} else {
			c.logger.Warn("bifrost commit answered 200 with a report that cannot be read", "import_id", id, "error", err)
		}
		return &commitReport{ImportID: id}, nil
	}
	return &rep, nil
}

// abort abandons an import. It is best effort: Bifrost drops an untouched
// import after an hour anyway, and answers 404 for one that is already gone
// (a failed commit, an expiry), which is the outcome wanted.
func (c *bifrostClient) abort(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abortTimeout)
	defer cancel()
	status, _, _, err := c.do(ctx, http.MethodDelete, importPath(id), nil, nil)
	switch {
	case err != nil:
		c.logger.Warn("bifrost import could not be abandoned; it expires on its own", "import_id", id, "error", err)
	case status == http.StatusNoContent || status == http.StatusNotFound || status == http.StatusOK:
	default:
		c.logger.Warn("bifrost import could not be abandoned; it expires on its own", "import_id", id, "status", status)
	}
}

func importPath(id string) string { return importsPath + "/" + url.PathEscape(id) }

// do sends one request. A transport failure comes back as a Retriable error
// with a fixed text (safeurl.UserMessage): the raw one names addresses and the
// URL.
func (c *bifrostClient) do(ctx context.Context, method, path string, body []byte, extra map[string]string) (int, []byte, http.Header, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return 0, nil, nil, sdk.Retriable(errors.New("bifrost request cannot be built; check base_url"))
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.tenantID != "" {
		req.Header.Set("X-Tenant-Id", c.tenantID)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.logger.Warn("bifrost request failed", "method", method, "path", path, "error", safeurl.Detail(err))
		text, ok := safeurl.UserMessage(err)
		if !ok {
			text = "request failed"
		}
		return 0, nil, nil, sdk.Retriable(&userError{msg: "bifrost " + opName(method, path) + ": " + text, cause: err})
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, nil, sdk.Retriable(&userError{msg: "bifrost " + opName(method, path) + ": answer could not be read", cause: err})
	}
	return resp.StatusCode, data, resp.Header, nil
}

func opName(method, path string) string {
	switch {
	case method == http.MethodDelete:
		return "abort"
	case strings.HasSuffix(path, "/items"):
		return "items"
	case strings.HasSuffix(path, "/commit"):
		return "commit"
	}
	return "begin"
}

// classify turns a non-success answer into the SDK's retry signal.
//
//   - 429: too many imports open at once. Retried after Retry-After.
//   - 401, 403: the node's token, its permission (catalogue.import) or its
//     tenant header is wrong. A fix to the node brings the message back, so it
//     is retried and then kept in the DLQ (#734), not dropped.
//   - 404: no such import (expired, or the server restarted: imports live in
//     memory) or, on begin, a base_url that is not the catalogue-api. The next
//     attempt opens a new import, so it is retried.
//   - 5xx and anything unexpected: retried.
//   - any other 4xx (400, 413, 422: the shrink guard, a file that is mostly
//     bad rows, validation): the request was understood and refused, and the
//     same payload is refused again. Permanent, with Bifrost's reason.
func (c *bifrostClient) classify(op string, status int, body []byte, hdr http.Header) error {
	reason := c.reason(body)
	switch {
	case status == http.StatusTooManyRequests:
		return sdk.RateLimited(fmt.Errorf("bifrost %s 429: %s", op, reason), retryAfter(hdr))
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return sdk.Retriable(fmt.Errorf("bifrost %s %d: %s; check the node's token, its catalogue.import permission and tenant_id, then retry the message from the DLQ", op, status, reason))
	case status == http.StatusNotFound:
		return sdk.Retriable(fmt.Errorf("bifrost %s 404: %s", op, reason))
	case status == http.StatusRequestEntityTooLarge || (status == http.StatusBadRequest && op == "items"):
		// We always send well-formed JSON, so this is a proxy with a body limit
		// (ingress-nginx defaults to 1 MiB) or an item with invalid UTF-8,
		// which serde_json refuses with a 400.
		// Node config, so retried and kept in the DLQ (#734).
		return sdk.Retriable(fmt.Errorf("bifrost %s %d: %s; either a proxy in front of Bifrost refused the request size (lower batch_size) or an item is not valid UTF-8/JSON (fix the source); then retry the message from the DLQ", op, status, reason))
	case status >= 400 && status < 500:
		return sdk.Permanent(fmt.Errorf("bifrost %s %d: %s", op, status, reason))
	default:
		return sdk.Retriable(fmt.Errorf("bifrost %s %d: %s", op, status, reason))
	}
}

// reason is Bifrost's own explanation for a refusal: the "error" member of
// its JSON body ({"error": "…"}), or the body as text. The token is cut out in
// case something echoes the Authorization header, control characters are
// dropped and the length is capped.
func (c *bifrostClient) reason(body []byte) string {
	var j struct {
		Error string `json:"error"`
	}
	text := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &j) == nil && strings.TrimSpace(j.Error) != "" {
		text = j.Error
	}
	if c.token != "" {
		text = strings.ReplaceAll(text, c.token, "[redacted]")
	}
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "no reason given"
	}
	if r := []rune(text); len(r) > maxReasonRunes {
		text = string(r[:maxReasonRunes]) + "…"
	}
	return text
}

// retryAfter reads Retry-After in seconds; without it, a few seconds. The SDK
// clamps the wait to 1 s .. 2 min.
func retryAfter(h http.Header) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 5 * time.Second
}
