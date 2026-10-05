package managementapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// The management API reaches Postgres through database/sql and the "pgx"
// driver. Unit tests use sqlmock and never touch a driver, so nothing else in
// this package can tell one driver version from another. This test can: it
// runs the real repository against a real Postgres and compares what the API
// would serialise with a snapshot recorded on the driver in use before the
// pgx v4 → v5 move (plans/pgx-v5.md). A driver change that alters a value's
// type, a timestamp's zone, an array, a NULL or an error class shows up as a
// diff here rather than in production.
//
// It needs a Postgres it may create a database on:
//
//	MGMT_DRIVER_TEST_DB_URL=postgres://postgres:…@localhost:5432/postgres?sslmode=disable
//
// and skips itself without one. Re-record with -update-driver-snapshot.
var updateDriverSnapshot = flag.Bool("update-driver-snapshot", false, "rewrite testdata/driver_snapshot.json")

const driverSnapshotFile = "testdata/driver_snapshot.json"

// freshDatabase creates an empty database with every migration applied and
// returns its URL. Migrations go through lib/pq so that the driver under test
// is not also the thing that built the schema.
func freshDatabase(t *testing.T) string {
	t.Helper()
	adminURL := os.Getenv("MGMT_DRIVER_TEST_DB_URL")
	if adminURL == "" {
		t.Skip("MGMT_DRIVER_TEST_DB_URL not set — needs a real Postgres")
	}
	admin, err := sql.Open("postgres", adminURL)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()
	name := "mgmt_driver_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	if _, err := admin.Exec(`CREATE DATABASE ` + pq.QuoteIdentifier(name)); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		a, err := sql.Open("postgres", adminURL)
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.Exec(`DROP DATABASE IF EXISTS ` + pq.QuoteIdentifier(name) + ` WITH (FORCE)`)
	})

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("parse MGMT_DRIVER_TEST_DB_URL: %v", err)
	}
	u.Path = "/" + name
	dbURL := u.String()

	schema, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("open schema connection: %v", err)
	}
	defer schema.Close()
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "infrastructure", "migrations", "*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := schema.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(f), err)
		}
	}
	return dbURL
}

var (
	uuidRe = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	// A timestamp the database stamped itself (NOW()): its instant varies per
	// run, its zone must not. Fixed test timestamps are all on 2026-01-02 and
	// are left alone, so their zone AND their precision are compared.
	nowRe = regexp.MustCompile(`"(20\d\d-\d\d-\d\dT[0-9:.]+)(Z|[+-]\d\d:\d\d)"`)
)

// normalise makes a snapshot comparable between runs: generated ids become
// <uuid-N> in order of appearance, database-stamped times keep only their zone.
func normalise(raw []byte) string {
	seen := map[string]string{}
	out := uuidRe.ReplaceAllStringFunc(string(raw), func(id string) string {
		if _, ok := seen[id]; !ok {
			seen[id] = fmt.Sprintf("<uuid-%d>", len(seen)+1)
		}
		return seen[id]
	})
	return nowRe.ReplaceAllStringFunc(out, func(m string) string {
		if strings.Contains(m, "2026-01-02T") {
			return m
		}
		return `"<now` + nowRe.FindStringSubmatch(m)[2] + `>"`
	})
}

func TestDriver_RepositoryBehaviourIsPinned(t *testing.T) {
	dbURL := freshDatabase(t)

	// A fixed, non-UTC local zone: a driver that hands back times in Local
	// and one that hands them back in UTC then differ visibly, and the same
	// way on every machine.
	prevLocal := time.Local
	time.Local = time.FixedZone("TEST", 3*3600)
	t.Cleanup(func() { time.Local = prevLocal })

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open with the pgx driver: %v", err)
	}
	defer db.Close()
	repo := NewPostgresRepository(db)
	ctx := context.Background()
	at := time.Date(2026, 1, 2, 3, 4, 5, 678901000, time.UTC) // microseconds: what timestamptz keeps
	snap := map[string]any{}
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}

	// --- users: UUID keys, booleans, NULL timestamps, unique violation (string check)
	user := &User{ID: uuid.NewString(), Email: "driver@example.test", PasswordHash: "h", FullName: "Driver Test",
		Status: "active", EmailVerified: true, CreatedAt: at, UpdatedAt: at}
	must("create user", repo.CreateUser(ctx, user))
	gotUser, err := repo.GetUserByEmail(ctx, user.Email)
	must("get user", err)
	snap["user"] = gotUser
	dup := *user
	dup.ID = uuid.NewString()
	snap["duplicate_email_is_ErrEmailExists"] = errors.Is(repo.CreateUser(ctx, &dup), auth.ErrEmailExists)

	// --- tenants: RETURNING into a struct, NULL text, transaction, unique slug (string check)
	tenant, err := repo.CreateTenant(ctx, user.ID, "Driver WS", "driver-ws")
	must("create tenant", err)
	snap["tenant"] = tenant
	_, err = repo.CreateTenant(ctx, user.ID, "Driver WS 2", "driver-ws")
	snap["duplicate_slug_is_ErrSlugAlreadyExists"] = errors.Is(err, ErrSlugAlreadyExists)
	role, err := repo.GetUserTenantRole(ctx, user.ID, tenant.ID)
	must("role", err)
	snap["owner_role"] = role

	// --- sessions: INET in and out, NULL varchar
	ip := "203.0.113.7"
	sess := &Session{ID: uuid.NewString(), UserID: user.ID, TokenHash: "tok-hash", IPAddress: &ip,
		CreatedAt: at, ExpiresAt: at.Add(24 * time.Hour), LastActivity: at, IsActive: true}
	must("create session", repo.CreateSession(ctx, sess))
	gotSess, err := repo.GetSessionByTokenHash(ctx, "tok-hash")
	must("get session", err)
	snap["session"] = gotSess

	// --- connections: JSONB graph in and out, NULL timestamps and text,
	//     and the one typed unique-violation check (*pgconn.PgError, 23505)
	conn := &Connection{ID: uuid.NewString(), TenantID: tenant.ID, Name: "Catalogue", Description: "d",
		Nodes: []*Node{
			{ID: "n1", Type: "consumer", Enabled: true, Config: json.RawMessage(`{"type":"business_central","business_central":{"entity":"items","pictures":true,"page_size":50}}`)},
			{ID: "n2", Type: "producer", Enabled: true, Config: json.RawMessage(`{"type":"remote_agent","remote_agent":{"target":"group","group":"all-tills","directory":"catalogue-in"}}`)},
		},
		Edges:  []*Edge{{ID: "e1"}},
		Status: "stopped", CreatedAt: at, UpdatedAt: at}
	must("create connection", repo.CreateConnection(ctx, conn))
	gotConn, err := repo.GetConnection(ctx, conn.ID)
	must("get connection", err)
	snap["connection"] = gotConn
	same := *conn
	same.ID = uuid.NewString()
	var conflict *ConflictError
	snap["duplicate_connection_name_is_ConflictError"] = errors.As(repo.CreateConnection(ctx, &same), &conflict)
	lastErr := "401 from Business Central"
	must("update status", repo.UpdateConnectionStatus(ctx, conn.ID, "error", &lastErr))
	list, total, err := repo.ListConnections(ctx, tenant.ID, &ListFilters{Status: "error", Limit: 10})
	must("list connections", err)
	snap["connections_in_error"] = map[string]any{"total": total, "items": list}

	// --- connection events: JSONB payloads, ordering
	must("event", repo.CreateConnectionEvent(ctx, &ConnectionEvent{ID: uuid.NewString(), ConnectionID: conn.ID, TenantID: tenant.ID,
		EventType: "error", EventData: json.RawMessage(`{"message":"401","attempt":3,"nested":{"ok":false}}`), CreatedAt: at}))
	events, err := repo.GetConnectionEvents(ctx, conn.ID)
	must("events", err)
	snap["events"] = events

	// --- remote agents: TEXT[] through pq.Array on this driver, JSONB
	//     directories, unnest + COUNT FILTER, an agent written the way the
	//     gateway writes it (lib/pq) and read back through the API's driver
	tok, err := repo.CreateAgentRegistrationToken(ctx, tenant.ID, "POS-PC", []string{"all-tills", "store-oslo"}, user.ID)
	must("registration token", err)
	tok.Token = "" // random
	snap["registration_token"] = tok
	gw, err := sql.Open("postgres", dbURL)
	must("gateway connection", err)
	defer gw.Close()
	var agentID string
	must("insert agent", gw.QueryRow(`
		INSERT INTO agents (tenant_id, name, hostname, os, arch, agent_version, credential_hash, directories, last_seen_at, groups)
		VALUES ($1, 'POS-PC', 'pos-pc', 'windows', 'amd64', '1.0', 'cred-hash', '[{"name":"catalogue-in","mode":"write"}]', $2, $3)
		RETURNING id::text`, tenant.ID, at, pq.Array([]string{"all-tills"})).Scan(&agentID))
	agents, err := repo.ListAgents(ctx, tenant.ID)
	must("list agents", err)
	snap["agents"] = agents
	updated, err := repo.SetAgentGroups(ctx, tenant.ID, agentID, []string{"store-oslo", "all-tills", "with space"})
	must("set groups", err)
	snap["agent_after_set_groups"] = updated
	cleared, err := repo.SetAgentGroups(ctx, tenant.ID, agentID, []string{})
	must("clear groups", err)
	snap["agent_groups_when_empty"] = cleared.Groups
	_, err = repo.SetAgentGroups(ctx, tenant.ID, agentID, []string{"all-tills", "store-oslo"})
	must("set groups again", err)
	groups, err := repo.ListAgentGroups(ctx, tenant.ID)
	must("list groups", err)
	snap["agent_groups"] = groups

	// --- notification targets: JSONB config struct
	target := &NotificationTarget{TenantID: tenant.ID, Name: "ops-mail", Type: "email", Enabled: true,
		Config: NotificationTargetConfig{Email: "ops@example.test", Platform: true, MinSeverity: "warning"}}
	must("create target", repo.CreateNotificationTarget(ctx, target, ""))
	targets, err := repo.ListNotificationTargets(ctx, tenant.ID)
	must("list targets", err)
	snap["notification_targets"] = targets

	// --- audit log: JSONB map, INET as text, NULL uuid, filters with mixed argument types
	must("audit", repo.CreateAuditEntry(ctx, &AuditEntry{TenantID: tenant.ID, UserID: &user.ID, ActorKind: "user", ActorLabel: "driver@example.test",
		Action: "connection.start", ResourceType: "connection", ResourceID: conn.ID, Method: "POST", Path: "/api/v1/connections/x/start",
		StatusCode: 200, RequestID: "req-1", IPAddress: "203.0.113.7", UserAgent: "test",
		Details: map[string]interface{}{"connection_id": conn.ID, "n": 2, "ok": true}, OccurredAt: at}))
	must("audit without a user", repo.CreateAuditEntry(ctx, &AuditEntry{TenantID: tenant.ID, ActorKind: "api_key", ActorLabel: "key",
		Action: "connection.resend", Method: "POST", Path: "/p", StatusCode: 202, OccurredAt: at.Add(time.Second)}))
	since := at.Add(-time.Hour)
	entries, n, err := repo.ListAuditEntries(ctx, tenant.ID, AuditFilters{Since: &since}, 10, 0)
	must("list audit", err)
	snap["audit"] = map[string]any{"total": n, "entries": entries}

	// --- workspace API key: upsert, lookup by hash
	_, err = repo.UpsertTenantAPIKey(ctx, tenant.ID, "hash-1")
	must("api key", err)
	key2, err := repo.UpsertTenantAPIKey(ctx, tenant.ID, "hash-2")
	must("api key rotate", err)
	snap["api_key_after_rotate"] = key2
	owner, err := repo.GetTenantByAPIKeyHash(ctx, "hash-2")
	must("tenant by key", err)
	snap["tenant_by_api_key"] = owner.Slug
	old, _ := repo.GetTenantByAPIKeyHash(ctx, "hash-1")
	snap["old_api_key_still_valid"] = old != nil

	raw, err := json.MarshalIndent(snap, "", "  ")
	must("marshal snapshot", err)
	got := normalise(raw) + "\n"

	if *updateDriverSnapshot {
		must("mkdir testdata", os.MkdirAll("testdata", 0o755))
		must("write snapshot", os.WriteFile(driverSnapshotFile, []byte(got), 0o644))
		t.Logf("wrote %s", driverSnapshotFile)
		return
	}
	want, err := os.ReadFile(driverSnapshotFile)
	if err != nil {
		t.Fatalf("read %s (record it with -update-driver-snapshot): %v", driverSnapshotFile, err)
	}
	if got != string(want) {
		t.Errorf("what the API reads and writes through the database driver has changed.\n%s", lineDiff(string(want), got))
	}
}

// lineDiff shows the lines that differ between two snapshots.
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			fmt.Fprintf(&b, "line %d\n  want: %s\n  got:  %s\n", i+1, wl, gl)
		}
	}
	return b.String()
}
