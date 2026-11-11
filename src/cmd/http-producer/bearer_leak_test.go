package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// #850: the bearer token of an auth_type=bearer node goes to the target and
// nowhere else. Not in a log line, not in the error that becomes the DLQ
// copy's X-Vrsky-Last-Error, and not in the activity feed, whatever the
// target answers. These run on sqlmock with the secret encrypted under a test
// key, so they need no database.

const bearerSecretID = "00000000-0000-0000-0000-0000000008a0"

// syncBuffer is a bytes.Buffer safe for the producer's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// sealFakeToken sets a test master key and returns fakeToken encrypted under
// it, as the secrets table holds it.
func sealFakeToken(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	key := hex.EncodeToString(k)
	t.Setenv(crypto.KeyEnvVar, key)
	ct, err := crypto.Encrypt(fakeToken, key)
	if err != nil {
		t.Fatal(err)
	}
	return ct
}

// mockBearerConn serves connID with one HTTP node whose http block is
// httpBlock, and the bearer secret for tenant-1 only. Repeated, because the
// harness may read more than once.
func mockBearerConn(t *testing.T, connID, httpBlock string) *sql.DB {
	t.Helper()
	ct := sealFakeToken(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	nodes := `[{"id":"dst","type":"producer","config":{"type":"http","http":` + httpBlock + `}}]`
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 5; i++ {
		mock.ExpectQuery("FROM connections WHERE id").WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))
		mock.ExpectQuery("FROM secrets").WithArgs(bearerSecretID, "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"ciphertext"}).AddRow(ct))
	}
	return db
}

// assertNoToken fails when fakeToken, or a recognisable piece of it, is in
// the producer's log output, its activity feed for connID, or err.
func assertNoToken(t *testing.T, what string, p *httpProducer, connID string, logs fmt.Stringer, err error) {
	t.Helper()
	events, jerr := json.Marshal(p.getRecentEvents(connID))
	if jerr != nil {
		t.Fatal(jerr)
	}
	places := map[string]string{"log output": logs.String(), "activity feed": string(events)}
	if err != nil {
		places["error (X-Vrsky-Last-Error)"] = err.Error()
	}
	for where, text := range places {
		// The middle of the token, so a cut-off or partly masked copy counts.
		if strings.Contains(text, fakeToken[5:25]) {
			t.Errorf("%s: the bearer token is in the %s: %s", what, where, text)
		}
	}
}

// echoAuth answers with the request's Authorization header in the body, the
// way httpbin and many test endpoints echo headers back.
func echoAuth(r *http.Request) string {
	return `{"headers":{"Authorization":"` + r.Header.Get("Authorization") + `"}}`
}

func TestBearer_TokenIsNeverShown(t *testing.T) {
	cases := []struct {
		name         string
		status       int    // what the target answers; 0: no target
		url          string // for status 0
		allowPrivate bool
		echo         bool // the target echoes the Authorization header
		stream       bool // delivered through DeliverStream
		wantErr      string
		wantSent     bool // the target got the token
	}{
		{name: "2xx echoing the header", status: http.StatusOK, echo: true, allowPrivate: true, wantSent: true},
		{name: "2xx streamed, echoing the header", status: http.StatusOK, echo: true, stream: true, allowPrivate: true, wantSent: true},
		{name: "401 echoing the header", status: http.StatusUnauthorized, echo: true, allowPrivate: true, wantErr: "upstream 401", wantSent: true},
		{name: "403", status: http.StatusForbidden, allowPrivate: true, wantErr: "upstream 403", wantSent: true},
		{name: "5xx echoing the header", status: http.StatusBadGateway, echo: true, allowPrivate: true, wantErr: "upstream 502", wantSent: true},
		{name: "4xx echoing the header (acked)", status: http.StatusUnprocessableEntity, echo: true, allowPrivate: true, wantSent: true},
		{name: "connection refused", url: "http://" + closedPort(t) + "/in", allowPrivate: true, wantErr: "transport"},
		{name: "private address refused", url: "http://10.0.0.5/in", wantErr: "destination refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := tc.url
			var target *authRecorder
			if tc.status != 0 {
				var body func(*http.Request) string
				if tc.echo {
					body = echoAuth
				}
				target = newAuthRecorder(t, tc.status, body)
				url = target.srv.URL
			}
			const connID = "conn-bearer-leak"
			db := mockBearerConn(t, connID, fmt.Sprintf(`{"url":%q,"method":"POST","auth_type":"bearer","token_secret_id":%q}`, url, bearerSecretID))
			var logs syncBuffer
			p := newBearerProducer(db, &logs)
			p.allowPrivate = tc.allowPrivate
			env := fanOutEnv(connID)

			var err error
			if tc.stream {
				env.PayloadSize = int64(len(env.Payload))
				err = p.DeliverStream(context.Background(), env, bytes.NewReader(env.Payload))
			} else {
				err = p.Deliver(context.Background(), env)
			}

			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("delivery = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("delivery = %v, want an error with %q", err, tc.wantErr)
			case err != nil && sdk.IsPermanent(err):
				t.Fatalf("delivery = %v, want it retried, not dropped", err)
			}
			if tc.wantSent {
				if got := target.seen(); len(got) != 1 || got[0] != "Bearer "+fakeToken {
					t.Fatalf("target saw Authorization %q, want the bearer token once", got)
				}
			}
			assertNoToken(t, tc.name, p, connID, &logs, err)
			if tc.echo {
				// The answer is still shown, with the token masked.
				events, _ := json.Marshal(p.getRecentEvents(connID))
				if !strings.Contains(string(events), `Bearer `+strings.Repeat("*", len(fakeToken))) {
					t.Errorf("activity feed lost the masked answer: %s", events)
				}
			}
		})
	}
}

// A token written into the node instead of stored as a secret is refused, as
// is auth_type bearer with no token. Nothing is sent, the message is retried
// and then kept in the DLQ, and the error names the setting, never the value.
func TestBearer_TokenWrittenIntoTheNodeIsRefused(t *testing.T) {
	cases := []struct {
		name  string
		block string // the http block after "url"
		want  string
	}{
		{name: "inline token", block: `"auth_type":"bearer","token":%q`, want: "it must be stored as a secret (http.token_secret_id)"},
		{name: "inline token beside a stored one", block: `"auth_type":"bearer","token":%q,"token_secret_id":"` + bearerSecretID + `"`, want: "http.token_secret_id"},
		{name: "inline token, auth_type none", block: `"auth_type":"none","token":%q`, want: "http.token_secret_id"},
		{name: "inline token as a placeholder", block: `"auth_type":"bearer","token":"{secret:` + bearerSecretID + `}"`, want: "http.token_secret_id"},
		{name: "inline token, mixed-case keys", block: `"Auth_Type":"bearer","Token":%q`, want: "it must be stored as a secret (http.token_secret_id)"},
		{name: "bearer without a token", block: `"auth_type":"bearer"`, want: "has no token; set http.token_secret_id"},
		{name: "bearer with an empty token", block: `"auth_type":"bearer","token":""`, want: "has no token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := newAuthRecorder(t, http.StatusOK, nil)
			block := tc.block
			if strings.Contains(block, "%q") {
				block = fmt.Sprintf(block, fakeToken)
			}
			const connID = "conn-bearer-inline"
			db := mockBearerConn(t, connID, fmt.Sprintf(`{"url":%q,%s}`, target.srv.URL, block))
			var logs syncBuffer
			p := newBearerProducer(db, &logs)

			err := p.Deliver(context.Background(), fanOutEnv(connID))
			if err == nil || sdk.IsPermanent(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Deliver = %v, want a retried error with %q", err, tc.want)
			}
			if got := target.seen(); len(got) != 0 {
				t.Fatalf("target got %d requests, want none: the node's token is not usable", len(got))
			}
			assertNoToken(t, tc.name, p, connID, &logs, err)
		})
	}
}

// loggedProducer runs the real producer under the SDK harness with its log
// output captured: the harness hands Configure the SDK's own logger.
type loggedProducer struct {
	*httpProducer
	logs *syncBuffer
}

func (l loggedProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if err := l.httpProducer.Configure(ctx, res); err != nil {
		return err
	}
	l.logger = slog.New(slog.NewTextHandler(l.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return nil
}

// A 401 or 403 with a stored bearer follows #838's rule: retried on the usual
// backoff, each attempt with the token, then kept in the DLQ. The DLQ copy's
// X-Vrsky-Last-Error says why, without the token.
func TestBearer_401And403AreRetriedThenDeadLettered(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			allowLocalTarget(t)
			var hits atomic.Int32
			target := newAuthRecorder(t, status, func(r *http.Request) string { hits.Add(1); return echoAuth(r) })
			const connID = "conn-bearer-dlq"
			db := mockBearerConn(t, connID, fmt.Sprintf(`{"url":%q,"auth_type":"bearer","token_secret_id":%q}`, target.srv.URL, bearerSecretID))
			logs := &syncBuffer{}
			p := &httpProducer{}
			h := harness.NewProducerHarness(t, loggedProducer{p, logs}, harness.Options{Name: "http-producer", DB: db, Backoff: fastBackoff})
			lastErr := dlqErrors(t, h.NATS())
			env := fanOutEnv(connID)
			h.Publish(t, env)

			if got := h.ExpectDLQ(t, 15*time.Second); got.ID != env.ID {
				t.Fatalf("DLQ holds %s, want %s", got.ID, env.ID)
			}
			var last error
			select {
			case e := <-lastErr:
				if want := fmt.Sprintf("upstream %d", status); !strings.Contains(e, want) {
					t.Errorf("X-Vrsky-Last-Error = %q, want it to contain %q", e, want)
				}
				last = fmt.Errorf("%s", e)
			case <-time.After(5 * time.Second):
				t.Fatal("no X-Vrsky-Last-Error on the DLQ copy")
			}
			if n := hits.Load(); n != 5 {
				t.Errorf("target got %d attempts, want 5 before the DLQ", n)
			}
			for _, a := range target.seen() {
				if a != "Bearer "+fakeToken {
					t.Errorf("an attempt carried Authorization %q, want the bearer token", a)
				}
			}
			assertNoToken(t, "DLQ", p, connID, logs, last)
		})
	}
}

func TestResponsePreview_MasksTheTokenWhereverItFalls(t *testing.T) {
	const n = 64
	tok := "tok-0123456789abcdef"
	mask := strings.Repeat("*", len(tok))
	cases := []struct {
		name   string
		body   string
		tokens []string
		want   string
	}{
		{name: "no token sent", body: "hello " + tok, want: "hello " + tok},
		{name: "at the start", body: tok + " rest", tokens: []string{tok}, want: mask + " rest"},
		{name: "twice", body: tok + "/" + tok, tokens: []string{tok}, want: mask + "/" + mask},
		{name: "straddling the cut", body: strings.Repeat("a", n-5) + tok + "zzz", tokens: []string{tok}, want: strings.Repeat("a", n-5) + mask[:5]},
		{name: "just past the cut", body: strings.Repeat("a", n) + tok, tokens: []string{tok}, want: strings.Repeat("a", n)},
		{name: "an OAuth token and its refresh", body: "old " + tok + " new " + tok + "2", tokens: []string{tok, tok + "2"}, want: "old " + mask + " new " + mask + "*"},
		{name: "long answer is cut", body: strings.Repeat("b", 3*n), tokens: []string{tok}, want: strings.Repeat("b", n)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := responsePreview(strings.NewReader(tc.body), n, tc.tokens)
			if got != tc.want {
				t.Errorf("preview = %q, want %q", got, tc.want)
			}
			if len(tc.tokens) > 0 && strings.Contains(got, tok[:6]) {
				t.Errorf("preview %q shows part of the token", got)
			}
		})
	}
}
