package harness

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// TestSecretID is the secret id the positive case's own node references (as
// "<field>_secret_id"), and TestSecret what it resolves to.
const (
	TestSecretID = "00000000-0000-0000-0000-0000000000aa"
	TestSecret   = "s3cret-from-the-store"
)

// ConfigReadSpec describes how a producer reads its connection config, for
// RunConfigReadCases.
type ConfigReadSpec struct {
	// New builds the producer under test on db, as Configure would.
	New func(t *testing.T, db *sql.DB) sdk.Producer
	// Columns the producer's connection query scans, in order: "nodes", or
	// "nodes" and "edges".
	Columns []string
	// OwnNode is the config of a producer node of this connector's type. When
	// the producer resolves secrets it holds a "<field>_secret_id" reference;
	// leave OwnNodeHasSecret false for a producer that resolves none.
	OwnNode          string
	OwnNodeHasSecret bool
	// ForeignNode is the config of another connector's producer node with a
	// "<field>_secret_id" reference.
	ForeignNode string
	// Positive builds the producer for the positive case, wired to a local
	// fake, and the config of its own node pointing at that fake. When
	// OwnNodeHasSecret, the node references TestSecretID; it resolves to
	// TestSecret. delivered reports whether the fake got the message with
	// TestSecret in hand.
	Positive func(t *testing.T, db *sql.DB) (p sdk.Producer, ownNode string, delivered func() bool)
}

// RunConfigReadCases checks the split #704 is about. A failed config or secret
// read must never ack: ServesConnection (when the producer implements it)
// answers true, and Deliver and DeliverStream (when implemented) return a
// retriable error marked sdk.ErrConfigUnavailable. A connection that is gone,
// or has no node of this producer's type, is "not mine" and acks as before,
// even when another connector's node has an unreadable secret.
func RunConfigReadCases(t *testing.T, spec ConfigReadSpec) {
	t.Helper()
	errDown := errors.New("pq: the database system is shutting down")

	cases := []struct {
		name      string
		skip      bool
		expect    func(m sqlmock.Sqlmock) // the reads of one config lookup
		wantAcked bool
	}{
		{
			name: "connection query fails",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`FROM connections`).WillReturnError(errDown)
			},
		},
		{
			name: "own node's secret read fails",
			skip: !spec.OwnNodeHasSecret,
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`FROM connections`).WillReturnRows(connectionRow(spec.Columns, spec.OwnNode))
				m.ExpectQuery(`FROM secrets`).WillReturnError(errDown)
			},
		},
		{
			name: "connection does not exist",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`FROM connections`).WillReturnRows(sqlmock.NewRows(spec.Columns))
			},
			wantAcked: true,
		},
		{
			name: "only another connector's node, its secret unreadable",
			expect: func(m sqlmock.Sqlmock) {
				// No secrets query is expected: an unexpected one fails
				// with a sqlmock error, which would read as unavailable.
				m.ExpectQuery(`FROM connections`).WillReturnRows(connectionRow(spec.Columns, spec.ForeignNode))
			},
			wantAcked: true,
		},
	}

	t.Run("own node delivers next to another connector's unreadable secret", func(t *testing.T) {
		runPositiveCase(t, spec)
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skip {
				t.Skip("producer resolves no secrets")
			}
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer func() { _ = db.Close() }()
			p := spec.New(t, db)
			cs, scoped := p.(sdk.ConnectionScoped)
			sp, streams := p.(sdk.StreamingProducer)
			lookups := 1
			if scoped {
				lookups++
			}
			if streams {
				lookups++
			}
			for i := 0; i < lookups; i++ {
				tc.expect(mock)
			}

			ctx := context.Background()
			env := envelope.New()
			env.TenantID = "tenant-1"
			env.IntegrationID = "conn-1"
			env.Payload = []byte(`[{"id":1}]`)

			if scoped {
				if got := cs.ServesConnection(ctx, env.TenantID, env.IntegrationID); got == tc.wantAcked {
					t.Errorf("ServesConnection = %v, want %v", got, !tc.wantAcked)
				}
			}
			checkResult(t, "Deliver", p.Deliver(ctx, env), tc.wantAcked)
			if streams {
				checkResult(t, "DeliverStream", sp.DeliverStream(ctx, env, strings.NewReader(`[{"id":1}]`)), tc.wantAcked)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("config reads: %v", err)
			}
		})
	}
}

func checkResult(t *testing.T, what string, err error, wantAcked bool) {
	t.Helper()
	if wantAcked {
		if err != nil {
			t.Errorf("%s = %v, want nil (ack: not this producer's connection)", what, err)
		}
		return
	}
	switch {
	case err == nil:
		t.Errorf("%s = nil: the message would be acked and lost", what)
	case sdk.IsPermanent(err):
		t.Errorf("%s = Permanent(%v): the message would be acked and lost", what, err)
	case !sdk.IsConfigUnavailable(err):
		t.Errorf("%s = %v, want an error marked sdk.ErrConfigUnavailable", what, err)
	}
}

// runPositiveCase: the connection holds another connector's node whose secret
// can't be read (it is never queried) and this producer's own node, whose
// secret resolves. ServesConnection answers true and Deliver delivers.
func runPositiveCase(t *testing.T, spec ConfigReadSpec) {
	t.Helper()
	if spec.Positive == nil {
		t.Fatal("ConfigReadSpec.Positive is required")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyHex := hex.EncodeToString(key)
	t.Setenv(crypto.KeyEnvVar, keyHex)
	ciphertext, err := crypto.Encrypt(TestSecret, keyHex)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	p, ownNode, delivered := spec.Positive(t, db)
	expectLookup := func() {
		mock.ExpectQuery(`FROM connections`).WillReturnRows(connectionRow(spec.Columns, spec.ForeignNode, ownNode))
		if spec.OwnNodeHasSecret {
			mock.ExpectQuery(`FROM secrets`).WithArgs(TestSecretID, "tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"ciphertext"}).AddRow(ciphertext))
		}
	}

	ctx := context.Background()
	env := envelope.New()
	env.TenantID = "tenant-1"
	env.IntegrationID = "conn-1"
	env.ContentType = "application/json"
	env.Payload = []byte(`{"id":1}`)

	expectLookup()
	if cs, ok := p.(sdk.ConnectionScoped); ok {
		if !cs.ServesConnection(ctx, env.TenantID, env.IntegrationID) {
			t.Fatal("ServesConnection = false for a connection with this producer's node")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("config reads: %v", err)
		}
		// Deliver reads again, unless the producer cached the first answer.
		expectLookup()
	}
	if err := p.Deliver(ctx, env); err != nil {
		t.Fatalf("Deliver = %v, want nil", err)
	}
	if !delivered() {
		t.Error("the message did not reach the fake target with the resolved secret")
	}
}

// Upstream is a fake HTTP target for the positive case. It answers token
// requests (path ending in /token) and SAP's CSRF fetch, counts every other
// write, and notes whether TestSecret arrived in a header, Basic auth, the
// query or the body.
type Upstream struct {
	URL       string
	writes    atomic.Int32
	sawSecret atomic.Bool
}

// NewUpstream starts an Upstream, closed when t ends.
func NewUpstream(t *testing.T) *Upstream {
	t.Helper()
	u := &Upstream{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.check(r, body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"access_token":"tok","token_type":"Bearer","expires_in":3600}`)
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
			w.Header().Set("X-CSRF-Token", "csrf-1")
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{}`)
		default:
			u.writes.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"1","success":true}`)
		}
	}))
	t.Cleanup(srv.Close)
	u.URL = srv.URL
	return u
}

func (u *Upstream) check(r *http.Request, body []byte) {
	if _, pw, ok := r.BasicAuth(); ok && pw == TestSecret {
		u.sawSecret.Store(true)
	}
	for _, vs := range r.Header {
		for _, v := range vs {
			if strings.Contains(v, TestSecret) {
				u.sawSecret.Store(true)
			}
		}
	}
	if strings.Contains(r.URL.RawQuery, url.QueryEscape(TestSecret)) || strings.Contains(string(body), TestSecret) ||
		strings.Contains(string(body), url.QueryEscape(TestSecret)) {
		u.sawSecret.Store(true)
	}
}

// Writes is the number of writes that arrived.
func (u *Upstream) Writes() int32 { return u.writes.Load() }

// Delivered reports whether a write arrived and TestSecret was presented.
func (u *Upstream) Delivered() bool { return u.writes.Load() > 0 && u.sawSecret.Load() }

// connectionRow is a connections row holding one producer node per config.
func connectionRow(columns []string, configs ...string) *sqlmock.Rows {
	parts := make([]string, len(configs))
	for i, c := range configs {
		parts[i] = fmt.Sprintf(`{"id":"n%d","type":"producer","config":%s}`, i+1, c)
	}
	nodes := "[" + strings.Join(parts, ",") + "]"
	values := make([]driver.Value, len(columns))
	for i, c := range columns {
		switch c {
		case "nodes":
			values[i] = []byte(nodes)
		case "edges":
			values[i] = []byte(`[]`)
		default:
			values[i] = ""
		}
	}
	return sqlmock.NewRows(columns).AddRow(values...)
}
