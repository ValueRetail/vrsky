package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// #538 against a real database: the connection lookup, the status writes and
// the saved done set all go through Postgres (checkpoint.PostgresStore, what
// Configure wires up). Each start is a real one-shot start through
// startConnection on a new consumer (a new pod). Skipped without
// VRSKY_TEST_POSTGRES_URL.

const pdPGNodes = `[{"id":"c1","type":"consumer","config":{"type":"sftp","sftp":{"host":"sftp","username":"u","password":"p","remote_dir":"/in","after_action":"none","poll_interval_seconds":0}}}]`

type pdPG struct {
	t  *testing.T
	db *sql.DB
}

func newPDPG(t *testing.T) *pdPG {
	t.Helper()
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &pdPG{t: t, db: db}
}

// connection creates a tenant and an SFTP connection in it.
func (p *pdPG) connection() (tenant, conn string) {
	p.t.Helper()
	var owner string
	if err := p.db.QueryRow(`INSERT INTO users (email, password_hash, status)
		VALUES ('sftp-pd-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		p.t.Fatal(err)
	}
	if err := p.db.QueryRow(`INSERT INTO tenants (name, slug, owner_id)
		VALUES ('sftp-pd', 'sftp-pd-'||gen_random_uuid(), $1) RETURNING id`, owner).Scan(&tenant); err != nil {
		p.t.Fatal(err)
	}
	conn = uuid.NewString()
	if _, err := p.db.Exec(`INSERT INTO connections (id, tenant_id, name, status, nodes, edges)
		VALUES ($1, $2, $3, 'stopped', $4::jsonb, '[]'::jsonb)`, conn, tenant, "sftp-"+conn, pdPGNodes); err != nil {
		p.t.Fatal(err)
	}
	return tenant, conn
}

func (p *pdPG) status(conn string) string {
	p.t.Helper()
	var s string
	if err := p.db.QueryRow(`SELECT status FROM connections WHERE id = $1`, conn).Scan(&s); err != nil {
		p.t.Fatal(err)
	}
	return s
}

func (p *pdPG) rows(conn string) map[string]string {
	p.t.Helper()
	rows, err := p.db.Query(`SELECT node_id, tenant_id FROM connection_node_checkpoints WHERE connection_id = $1`, conn)
	if err != nil {
		p.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var node, tenant string
		if err := rows.Scan(&node, &tenant); err != nil {
			p.t.Fatal(err)
		}
		out[node] = tenant
	}
	if err := rows.Err(); err != nil {
		p.t.Fatal(err)
	}
	return out
}

// start runs one one-shot start of conn on a new consumer and waits for it to
// end "stopped".
func (p *pdPG) start(tenant, conn string, fake *fakeSFTP, log *pdLog) {
	p.t.Helper()
	c := &sftpConsumer{
		db:          p.db,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		active:      make(map[string]*activeConn),
		publish:     log.publish,
		dial:        func(*SFTPConfig) (sftpConn, error) { return fake, nil },
		checkpoints: checkpoint.NewPostgresStore(p.db),
		// The one-shot's second look (#562), without the 5 s wait.
		settleMin: 20 * time.Millisecond,
	}
	c.startConnection(context.Background(), conn, tenant)
	if got := p.status(conn); got != "running" {
		p.t.Fatalf("status after the start = %q, want running", got)
	}
	harness.Eventually(p.t, pdWait, "the one-shot to end", func() bool {
		c.mu.RLock()
		n := len(c.active)
		c.mu.RUnlock()
		return n == 0 && p.status(conn) == "stopped"
	})
}

func TestDonePersistPG_RestartAndTenants(t *testing.T) {
	p := newPDPG(t)
	tenantA, connA := p.connection()
	tenantB, connB := p.connection()
	fakeA, fakeB := &fakeSFTP{}, &fakeSFTP{}
	for _, f := range []*fakeSFTP{fakeA, fakeB} {
		pdPut(f, "x.json", `{"x":1}`, 0)
		pdPut(f, "y.json", `{"y":1}`, 0)
	}
	logA, logB := &pdLog{}, &pdLog{}

	p.start(tenantA, connA, fakeA, logA)
	requireCounts(t, logA, "A first start", map[string]int{"x.json": 1, "y.json": 1})
	if got := p.rows(connA); len(got) != 2 || got["c1"] != tenantA || got["c1#changes"] != tenantA {
		t.Fatalf("rows for A = %v, want the snapshot and changes rows, tenant A's", got)
	}

	// B: same node id, same file names, stamps and contents; A's set must
	// not stop B's publish.
	p.start(tenantB, connB, fakeB, logB)
	requireCounts(t, logB, "B first start", map[string]int{"x.json": 1, "y.json": 1})

	p.start(tenantA, connA, fakeA, logA)
	requireCounts(t, logA, "A restart", map[string]int{"x.json": 1, "y.json": 1})

	pdPut(fakeB, "y.json", `{"y":22}`, 0) // replaced while stopped
	pdPut(fakeB, "z.json", `{"z":1}`, 0)
	p.start(tenantB, connB, fakeB, logB)
	requireCounts(t, logB, "B restart", map[string]int{"x.json": 1, "y.json": 2, "z.json": 1})
	p.start(tenantB, connB, fakeB, logB)
	requireCounts(t, logB, "B second restart", map[string]int{"x.json": 1, "y.json": 2, "z.json": 1})
	requireCounts(t, logA, "A, untouched by B", map[string]int{"x.json": 1, "y.json": 1})
}
