package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/pgdsn"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// dbProducer writes pipeline envelopes as rows into external target databases.
// It is a connector built on the SDK: the runner owns NATS/JetStream, the
// durable subscription, the health server, signal handling and graceful
// shutdown; this type implements Configure (management DB + caches + the
// /events and /test-connection APIs), Deliver (write one envelope), and Stop
// (close the per-target SQL pools it opened).
type dbProducer struct {
	sdk.BaseProducer

	db     *sql.DB // management DB
	logger *slog.Logger
	nc     *nats.Conn
	cmdSub *nats.Subscription

	// Cache target DB connections per connection ID (multiple producers per connection).
	targetCache     map[string][]*TargetConnection
	targetCacheMu   sync.RWMutex
	targetCacheTime map[string]time.Time
	targetCacheTTL  time.Duration

	// SSE events + recent-event buffer.
	eventSubs      map[string][]chan DBProdEvent
	eventSubsMu    sync.RWMutex
	recentEvents   map[string][]DBProdEvent
	recentEventsMu sync.RWMutex

	// openTarget dials a target database from a DSN. Defaulted to a real
	// postgres opener in Configure; tests inject a mock so the connector can be
	// exercised without Docker.
	openTarget func(connStr string) (*sql.DB, error)

	// written remembers the targets that committed a message whose delivery
	// failed on another target, so its redelivery skips them (#705; see
	// writtenSet). Created on first use.
	written     *writtenSet
	writtenOnce sync.Once
}

type TargetDBConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Database string `json:"database"`
	SSLMode  string `json:"sslmode"`
	Table    string `json:"table"`
	Mode     string `json:"mode"` // "insert", "upsert", "create_insert"
}

// targetConnectTimeout bounds connecting to a target database (dial, TLS and
// sign-in), in seconds. Without it a host that drops packets holds the
// delivery for the OS connect timeout, minutes, on every attempt (#705). A
// variable so a test can shorten it.
var targetConnectTimeout = 10

// errTargetEvicted is returned for a target whose cached list was replaced
// or evicted while a delivery was using it. The delivery is retried and reads
// the current config.
var errTargetEvicted = errors.New("target config was reloaded during the delivery; will retry")

// TargetConnection is one database destination node of a connection.
type TargetConnection struct {
	NodeID         string
	Config         TargetDBConfig
	PredecessorID  string // direct predecessor node ID
	PredIsConsumer bool   // if true, process when _last_processed_by is empty

	// ConfigErr is set for a node that cannot be written to as configured
	// (no host, a config that does not parse). Deliver logs it at Error and
	// fails the message, so it is retried and dead-lettered (#705).
	ConfigErr error

	// DB is the target's pool, opened (and pinged) on first use by pool.
	// A failed open is not kept: the next delivery tries again.
	DB  *sql.DB
	dsn string
	mu  sync.Mutex
	// closed is set by close: the list this target was in has been evicted
	// or replaced, so pool must not open a pool nothing would ever close.
	closed bool
	// connecting is the open in flight, if any (guarded by mu).
	connecting *poolCall
}

// poolCall is one in-flight open of a target's pool. Concurrent first uses
// wait on it instead of opening a pool each.
type poolCall struct {
	done chan struct{}
	db   *sql.DB
	err  error
}

// pool returns the target's pool, opening and pinging it first if needed.
// The connect (up to the 10 s connect timeout) runs outside tc.mu, so close
// (eviction on the NATS command goroutine, Stop) never waits behind it. Only
// one caller connects; the others wait for its result. If the target was
// closed meanwhile the new pool is closed and the caller gets errTargetEvicted.
func (tc *TargetConnection) pool(ctx context.Context, open func(string) (*sql.DB, error)) (*sql.DB, error) {
	tc.mu.Lock()
	if tc.closed {
		tc.mu.Unlock()
		return nil, errTargetEvicted
	}
	if tc.DB != nil {
		db := tc.DB
		tc.mu.Unlock()
		return db, nil
	}
	if call := tc.connecting; call != nil {
		tc.mu.Unlock()
		select {
		case <-call.done:
			return call.db, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &poolCall{done: make(chan struct{})}
	tc.connecting = call
	tc.mu.Unlock()

	call.db, call.err = tc.connect(ctx, open)

	tc.mu.Lock()
	tc.connecting = nil
	if call.err == nil {
		if tc.closed {
			// Evicted while connecting: nothing would ever close this pool.
			_ = call.db.Close()
			call.db, call.err = nil, errTargetEvicted
		} else {
			tc.DB = call.db
		}
	}
	tc.mu.Unlock()
	close(call.done)
	return call.db, call.err
}

// connect opens and pings a pool. A failed open is closed and not kept.
func (tc *TargetConnection) connect(ctx context.Context, open func(string) (*sql.DB, error)) (*sql.DB, error) {
	db, err := open(tc.dsn)
	if err != nil {
		return nil, fmt.Errorf("open target database %s:%d: %w", tc.Config.Host, tc.Config.Port, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("reach target database %s:%d: %w", tc.Config.Host, tc.Config.Port, err)
	}
	return db, nil
}

// close closes the target's pool if it was opened, and keeps pool from
// opening another.
func (tc *TargetConnection) close() {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.closed = true
	if tc.DB != nil {
		_ = tc.DB.Close()
		tc.DB = nil
	}
}

type DBProdEvent struct {
	Type    string   `json:"type"` // "connected", "inserted", "created", "error", "info"
	Message string   `json:"message,omitempty"`
	Count   int      `json:"count,omitempty"`
	Time    string   `json:"time"`
	Payload string   `json:"payload,omitempty"` // data preview (truncated)
	Table   string   `json:"table,omitempty"`
	Columns []string `json:"columns,omitempty"`
}

func main() {
	if err := sdk.RunProducer(context.Background(), "db-producer", &dbProducer{}); err != nil {
		slog.Error("db-producer exited", "error", err)
		os.Exit(1)
	}
}

// Configure wires the producer's dependencies. Called once by the runner before
// the subscription starts.
func (p *dbProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("db-producer requires DATABASE_URL (per-connection config lives in the management connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	p.targetCache = make(map[string][]*TargetConnection)
	p.targetCacheTime = make(map[string]time.Time)
	if p.targetCacheTTL == 0 {
		p.targetCacheTTL = 5 * time.Minute
	}
	p.eventSubs = make(map[string][]chan DBProdEvent)
	p.recentEvents = make(map[string][]DBProdEvent)
	if p.openTarget == nil {
		p.openTarget = func(connStr string) (*sql.DB, error) { return sql.Open("postgres", connStr) }
	}

	// Serve the live SSE stream and the connection-test endpoint on the SDK
	// auxiliary HTTP port (WORKER_HTTP_PORT, 9500 in compose) via the
	// custom-handler hook — the UI uses both.
	p.RegisterHTTPHandler("/events/", p.eventsHandler())
	p.RegisterHTTPHandler("/test-connection/", testConnectionHandler())

	// Evict a connection's cached target pools on redeploy (#141).
	p.nc = res.NATS
	if p.nc != nil {
		sub, err := p.nc.Subscribe("vrsky.commands.*.connection.*", p.handleConnectionCommand)
		if err != nil {
			return fmt.Errorf("subscribe to connection commands: %w", err)
		}
		p.cmdSub = sub
	}

	p.logger.Info("db-producer configured")
	return nil
}

// evictTargetCache drops (and closes) a connection's cached target pools so the
// next message re-reads the connection config from the DB. Called on a
// start/stop command (redeploy) so config edits take effect immediately (#141).
func (p *dbProducer) evictTargetCache(connectionID string) {
	p.targetCacheMu.Lock()
	if tcs, ok := p.targetCache[connectionID]; ok {
		for _, tc := range tcs {
			tc.close()
		}
		delete(p.targetCache, connectionID)
		delete(p.targetCacheTime, connectionID)
	}
	p.targetCacheMu.Unlock()
}

// handleConnectionCommand evicts the target cache for the connection in a
// start/stop command.
func (p *dbProducer) handleConnectionCommand(msg *nats.Msg) {
	var cmd struct {
		ConnectionID string `json:"connection_id"`
	}
	if err := json.Unmarshal(msg.Data, &cmd); err != nil || cmd.ConnectionID == "" {
		return
	}
	p.evictTargetCache(cmd.ConnectionID)
	p.logger.Info("Evicted producer target cache on connection command", "connection_id", cmd.ConnectionID)
}

// Stop closes every target DB pool opened during message processing. The SDK
// runner calls this after the subscription has drained.
func (p *dbProducer) Stop(ctx context.Context) error {
	if p.cmdSub != nil {
		_ = p.cmdSub.Unsubscribe()
	}
	p.targetCacheMu.Lock()
	defer p.targetCacheMu.Unlock()
	for _, tcs := range p.targetCache {
		for _, tc := range tcs {
			tc.close()
		}
	}
	p.targetCache = make(map[string][]*TargetConnection)
	return nil
}

// Deliver writes one envelope into every matching target database configured
// for its connection, and acks (nil) only when every one of them has it.
//
//   - A payload that is not JSON is sdk.Permanent: dropped, retrying can't help.
//   - Anything that stops a target from taking the rows is returned so the
//     SDK NAKs it, retries and in the end dead-letters it with the error (#705):
//     a target that cannot be opened or reached, a row the database refuses
//     (constraint, type), a lost connection, and a node that cannot be written
//     to as configured (no host, a table name that is not `table` or
//     `schema.table`). The rows of one target go in one transaction, so a
//     failed target keeps none of them, and a target that committed is
//     skipped by the redelivery (see writtenSet).
//   - A connection with no database destination is not ours: acked. A failed
//     config read is not acked (#704).
func (p *dbProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	connectionID := env.IntegrationID
	if connectionID == "" {
		return nil
	}

	tcs, err := p.getTargetConnections(ctx, connectionID, env.TenantID)
	if err != nil {
		if sdk.IsConfigUnavailable(err) {
			return err // not acked: retried, then the DLQ
		}
		p.logger.Debug("No DB producer config", "connection_id", connectionID, "error", err)
		return nil
	}

	// Predecessor-based routing.
	lastProcessedBy := ""
	if env.Metadata != nil {
		if v, ok := env.Metadata["_last_processed_by"].(string); ok {
			lastProcessedBy = v
		}
	}
	var routed []*TargetConnection
	for _, tc := range tcs {
		if tc.PredIsConsumer && lastProcessedBy != "" {
			continue
		}
		if !tc.PredIsConsumer && tc.PredecessorID != "" && lastProcessedBy != tc.PredecessorID {
			continue
		}
		routed = append(routed, tc)
	}
	if len(routed) == 0 {
		return nil
	}

	rows, err := parseRows(env.Payload)
	if err != nil {
		p.emitEvent(connectionID, DBProdEvent{Type: "error", Message: err.Error(), Time: now()})
		p.logger.Error("dropping message", "error", err, "envelope_id", env.ID)
		return sdk.Permanent(err)
	}
	if len(rows) == 0 {
		return nil
	}

	written := p.writtenSet()
	at := time.Now()
	var failed []error
	var done []writtenKey // committed in this delivery or an earlier one
	var committed []writtenKey
	for _, tc := range routed {
		key := writtenKey{
			connID: connectionID, envelopeID: env.ID, nodeID: tc.NodeID,
			host: tc.Config.Host, port: tc.Config.Port, user: tc.Config.User,
			database: tc.Config.Database, table: tc.Config.Table,
		}
		if written.has(key, at) {
			p.logger.Info("Target already has this message; skipping it on redelivery",
				"connection_id", connectionID, "node_id", tc.NodeID, "envelope_id", env.ID)
			done = append(done, key)
			continue
		}
		if err := p.writeTarget(ctx, connectionID, tc, rows, env.Payload); err != nil {
			p.logger.Error("Not written to target database; message not acked, will retry",
				"connection_id", connectionID, "node_id", tc.NodeID, "table", tc.Config.Table,
				"envelope_id", env.ID, "error", err)
			p.emitEvent(connectionID, DBProdEvent{Type: "error", Message: err.Error(), Time: now(), Table: tc.Config.Table})
			failed = append(failed, fmt.Errorf("node %s: %w", tc.NodeID, err))
			continue
		}
		done = append(done, key)
		committed = append(committed, key)
	}
	if len(failed) > 0 {
		written.add(at, committed...)
		return sdk.Retriable(errors.Join(failed...))
	}
	written.forget(done...)
	return nil
}

// writtenSet returns the producer's skip set, creating it on first use.
func (p *dbProducer) writtenSet() *writtenSet {
	p.writtenOnce.Do(func() {
		if p.written == nil {
			p.written = newWrittenSet(writtenTTL, writtenMax)
		}
	})
	return p.written
}

// errBadPayload marks an envelope whose payload isn't JSON — a poison message.
var errBadPayload = errors.New("payload is not valid JSON")

// errBadTableName marks a destination whose configured table name is neither
// `table` nor `schema.table`.
var errBadTableName = errors.New("table name must be `table` or `schema.table`")

// errNoHost marks a database destination node without a host.
var errNoHost = errors.New("database destination has no host")

// parseRows reads a payload as one JSON object or an array of objects.
func parseRows(raw []byte) ([]map[string]interface{}, error) {
	payload := bytes.TrimSpace(raw)
	switch {
	case len(payload) > 0 && payload[0] == '[':
		var rows []map[string]interface{}
		if err := json.Unmarshal(payload, &rows); err != nil {
			return nil, fmt.Errorf("%w: %v", errBadPayload, err)
		}
		return rows, nil
	case len(payload) > 0 && payload[0] == '{':
		var single map[string]interface{}
		if err := json.Unmarshal(payload, &single); err != nil {
			return nil, fmt.Errorf("%w: %v", errBadPayload, err)
		}
		return []map[string]interface{}{single}, nil
	default:
		return nil, errBadPayload
	}
}

// writeTarget writes the rows to one target database in one transaction:
// the target ends up with all of them or none. Any error is the target's
// (config, open, ping, table check or create, a refused row, commit).
func (p *dbProducer) writeTarget(ctx context.Context, connectionID string, tc *TargetConnection, rows []map[string]interface{}, payload []byte) error {
	if tc.ConfigErr != nil {
		return tc.ConfigErr
	}
	table := tc.Config.Table
	schema, tableName, err := parseTableName(table)
	if err != nil {
		return err
	}
	mode := tc.Config.Mode
	if mode == "" {
		mode = "create_insert"
	}

	db, err := tc.pool(ctx, p.openTarget)
	if err != nil {
		return err
	}

	payloadPreview := string(bytes.TrimSpace(payload))
	if len(payloadPreview) > 3000 {
		payloadPreview = payloadPreview[:3000] + "..."
	}
	var columns []string
	for k := range rows[0] {
		columns = append(columns, k)
	}
	p.emitEvent(connectionID, DBProdEvent{Type: "info", Message: fmt.Sprintf("Writing %d rows to %s", len(rows), table), Time: now(), Payload: payloadPreview, Table: table, Columns: columns})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction on %s: %w", table, err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op after Commit

	if mode == "create_insert" {
		if err := p.ensureTable(ctx, tx, schema, tableName, rows[0]); err != nil {
			return fmt.Errorf("ensure table %q: %w", table, err)
		}
	}
	for i, row := range rows {
		if err := insertRow(ctx, tx, schema, tableName, row); err != nil {
			return fmt.Errorf("row %d of %d refused by %s: %w", i+1, len(rows), table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %d rows to %s: %w", len(rows), table, err)
	}

	p.logger.Info("Rows inserted", "connection_id", connectionID, "node_id", tc.NodeID, "table", table, "count", len(rows))
	p.emitEvent(connectionID, DBProdEvent{
		Type: "inserted", Message: fmt.Sprintf("Inserted %d/%d rows into %s", len(rows), len(rows), table),
		Count: len(rows), Time: now(), Payload: payloadPreview, Table: table, Columns: columns,
	})
	return nil
}

// ensureTable creates schema.table (or a bare table) from the sample row's keys
// unless it already exists. A bare name is looked up in public, as before.
func (p *dbProducer) ensureTable(ctx context.Context, tx *sql.Tx, schema, table string, sample map[string]interface{}) error {
	lookupSchema := schema
	if lookupSchema == "" {
		lookupSchema = "public"
	}
	var exists bool
	err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2)",
		lookupSchema, table).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	var cols []string
	for key, val := range sample {
		colType := "TEXT"
		switch val.(type) {
		case float64:
			colType = "DOUBLE PRECISION"
		case bool:
			colType = "BOOLEAN"
		}
		cols = append(cols, fmt.Sprintf("%s %s", quoteIdent(key), colType))
	}

	query := fmt.Sprintf("CREATE TABLE %s (%s)", qualifiedTable(schema, table), strings.Join(cols, ", "))
	if _, err = tx.ExecContext(ctx, query); err != nil {
		return err
	}

	p.logger.Info("Created table", "schema", schema, "table", table, "columns", len(cols))
	return nil
}

func insertRow(ctx context.Context, tx *sql.Tx, schema, table string, row map[string]interface{}) error {
	columns := make([]string, 0, len(row))
	placeholders := make([]string, 0, len(row))
	values := make([]interface{}, 0, len(row))

	i := 1
	for key, val := range row {
		columns = append(columns, quoteIdent(key))
		placeholders = append(placeholders, fmt.Sprintf("$%d", i))
		switch v := val.(type) {
		case map[string]interface{}, []interface{}:
			jsonBytes, _ := json.Marshal(v)
			values = append(values, string(jsonBytes))
		default:
			values = append(values, val)
		}
		i++
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		qualifiedTable(schema, table),
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "))

	_, err := tx.ExecContext(ctx, query, values...)
	return err
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// parseTableName splits a configured table name into schema and table. The
// name is taken literally: a dot always separates schema from table, so
// `orders` gives ("", "orders") and `public.orders` gives ("public", "orders").
// More than one dot, or an empty part, is errBadTableName.
func parseTableName(s string) (schema, table string, err error) {
	parts := strings.Split(s, ".")
	for _, part := range parts {
		if part == "" {
			return "", "", fmt.Errorf("%w (got %q)", errBadTableName, s)
		}
	}
	switch len(parts) {
	case 1:
		return "", parts[0], nil
	case 2:
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("%w (got %q)", errBadTableName, s)
	}
}

// qualifiedTable quotes table, prefixed by the quoted schema when there is one.
func qualifiedTable(schema, table string) string {
	if schema == "" {
		return quoteIdent(table)
	}
	return quoteIdent(schema) + "." + quoteIdent(table)
}

// getTargetConnections returns all DB-producer target connections for a
// connection (with a short cache). // lint:tenant-ok — connection lookup by PK; tenant scoping is enforced upstream when the pipeline is deployed.
func (p *dbProducer) getTargetConnections(ctx context.Context, connectionID, tenantID string) ([]*TargetConnection, error) {
	p.targetCacheMu.RLock()
	if tcs, ok := p.targetCache[connectionID]; ok {
		if time.Since(p.targetCacheTime[connectionID]) < p.targetCacheTTL {
			p.targetCacheMu.RUnlock()
			return tcs, nil
		}
	}
	p.targetCacheMu.RUnlock()

	var nodesJSON, edgesJSON []byte
	err := p.db.QueryRowContext(ctx, `SELECT nodes, edges FROM connections WHERE id = $1`, connectionID).Scan(&nodesJSON, &edgesJSON)
	if err != nil {
		return nil, sdk.ConfigReadError(err)
	}

	var nodes []struct {
		ID     string          `json:"id"`
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, fmt.Errorf("failed to parse nodes: %w", err)
	}

	var edges []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if edgesJSON != nil {
		_ = json.Unmarshal(edgesJSON, &edges)
	}

	reader := crypto.NewSQLSecretReader(p.db)
	var tcs []*TargetConnection
	for _, node := range nodes {
		if node.Type != "producer" {
			continue
		}
		// Decrypt any *_secret_id references (the UI stores the target DB
		// password as password_secret_id) so the typed TargetDBConfig below
		// sees plaintext. A failure is retried, never acked: better to fail
		// loud than dial with an empty password or drop the message. Only for
		// our own nodes: another connector's unreadable secret is not ours.
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(node.Config, &kind); err != nil || kind.Type != "database" {
			continue
		}
		resolved, rerr := crypto.ResolveSecretsInJSON(ctx, reader, tenantID, node.Config)
		if rerr != nil {
			return nil, sdk.ConfigUnavailable(fmt.Errorf("resolve secrets for node %s: %w", node.ID, rerr))
		}
		node.Config = resolved
		// The pool is opened on first use (TargetConnection.pool), so a target
		// that is down now is retried on the next delivery rather than left
		// out of the cached list. A node that is ours but cannot be written to
		// as configured stays in the list with ConfigErr, so Deliver fails the
		// message instead of skipping it (#705).
		tc := &TargetConnection{NodeID: node.ID}
		var nodeConfig struct {
			Database TargetDBConfig `json:"database"`
		}
		if err := json.Unmarshal(node.Config, &nodeConfig); err != nil {
			tc.ConfigErr = fmt.Errorf("node config does not parse: %w", err)
		} else if nodeConfig.Database.Host == "" {
			tc.ConfigErr = errNoHost
		}

		cfg := nodeConfig.Database
		if cfg.Port == 0 {
			cfg.Port = 5432
		}
		if cfg.SSLMode == "" {
			cfg.SSLMode = "disable"
		}
		tc.Config = cfg
		tc.dsn = pgdsn.Build(pgdsn.Config{
			Host: cfg.Host, Port: cfg.Port, User: cfg.User, Password: cfg.Password,
			Database: cfg.Database, SSLMode: cfg.SSLMode, ConnectTimeout: targetConnectTimeout,
		})

		var predID string
		var predIsConsumer bool
		for _, e := range edges {
			if e.Target == node.ID {
				predID = e.Source
				for _, n := range nodes {
					if n.ID == predID && n.Type == "consumer" {
						predIsConsumer = true
						break
					}
				}
				break
			}
		}

		tc.PredecessorID, tc.PredIsConsumer = predID, predIsConsumer
		tcs = append(tcs, tc)
	}

	if len(tcs) == 0 {
		return nil, fmt.Errorf("no database producer config found")
	}

	p.targetCacheMu.Lock()
	// Close the pools from the previous (now-stale) cache entry before
	// replacing it — a TTL refresh otherwise leaks the old *sql.DB pools
	// (sockets + goroutines) for the lifetime of the process. A delivery
	// in-flight against an old pool will at worst get a transient error and
	// be redelivered; refreshes are infrequent (default 5m).
	if old, ok := p.targetCache[connectionID]; ok {
		for _, tc := range old {
			tc.close()
		}
	}
	p.targetCache[connectionID] = tcs
	p.targetCacheTime[connectionID] = time.Now()
	p.targetCacheMu.Unlock()

	return tcs, nil
}

// --- Events (SSE) ---

func (p *dbProducer) subscribeEvents(connectionID string) (chan DBProdEvent, func()) {
	ch := make(chan DBProdEvent, 50)
	p.eventSubsMu.Lock()
	p.eventSubs[connectionID] = append(p.eventSubs[connectionID], ch)
	p.eventSubsMu.Unlock()
	return ch, func() {
		p.eventSubsMu.Lock()
		defer p.eventSubsMu.Unlock()
		subs := p.eventSubs[connectionID]
		for i, sub := range subs {
			if sub == ch {
				p.eventSubs[connectionID] = append(subs[:i], subs[i+1:]...)
				close(ch)
				break
			}
		}
	}
}

func (p *dbProducer) emitEvent(connectionID string, event DBProdEvent) {
	p.recentEventsMu.Lock()
	p.recentEvents[connectionID] = append(p.recentEvents[connectionID], event)
	if len(p.recentEvents[connectionID]) > 50 {
		p.recentEvents[connectionID] = p.recentEvents[connectionID][len(p.recentEvents[connectionID])-50:]
	}
	p.recentEventsMu.Unlock()

	p.eventSubsMu.RLock()
	defer p.eventSubsMu.RUnlock()
	for _, ch := range p.eventSubs[connectionID] {
		select {
		case ch <- event:
		default:
		}
	}
}

func (p *dbProducer) getRecentEvents(connectionID string) []DBProdEvent {
	p.recentEventsMu.RLock()
	defer p.recentEventsMu.RUnlock()
	cp := make([]DBProdEvent, len(p.recentEvents[connectionID]))
	copy(cp, p.recentEvents[connectionID])
	return cp
}

// --- HTTP handlers (served on the SDK auxiliary HTTP port) ---

// eventsHandler returns the /events/{connectionID} SSE handler.
func (p *dbProducer) eventsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		connectionID := strings.TrimPrefix(r.URL.Path, "/events/")
		connectionID = strings.TrimSuffix(connectionID, "/")
		if connectionID == "" {
			http.Error(w, "Missing connection ID", http.StatusBadRequest)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ch, unsub := p.subscribeEvents(connectionID)
		defer unsub()

		fmt.Fprintf(w, "data: {\"type\":\"connected\",\"message\":\"Listening for DB producer events\"}\n\n")
		for _, event := range p.getRecentEvents(connectionID) {
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		flusher.Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case event, ok := <-ch:
				if !ok {
					return
				}
				data, _ := json.Marshal(event)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}

// testConnectionHandler returns the /test-connection/ handler: it dials the
// supplied target DB and lists its public tables (used by the UI's
// connection-test button).
func testConnectionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var cfg TargetDBConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if cfg.Port == 0 {
			cfg.Port = 5432
		}
		if cfg.SSLMode == "" {
			cfg.SSLMode = "disable"
		}
		connStr := pgdsn.Build(pgdsn.Config{
			Host: cfg.Host, Port: cfg.Port, User: cfg.User, Password: cfg.Password,
			Database: cfg.Database, SSLMode: cfg.SSLMode, ConnectTimeout: 5,
		})
		db, err := sql.Open("postgres", connStr)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			resp, _ := json.Marshal(map[string]interface{}{"ok": false, "error": err.Error()})
			_, _ = w.Write(resp)
			return
		}
		defer db.Close()
		if err := db.Ping(); err != nil {
			w.Header().Set("Content-Type", "application/json")
			resp, _ := json.Marshal(map[string]interface{}{"ok": false, "error": err.Error()})
			_, _ = w.Write(resp)
			return
		}
		tables := []string{}
		rows, err := db.Query("SELECT table_name FROM information_schema.tables WHERE table_schema='public' ORDER BY table_name LIMIT 50")
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var t string
				if rows.Scan(&t) == nil {
					tables = append(tables, t)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		resp, _ := json.Marshal(map[string]interface{}{"ok": true, "tables": tables})
		_, _ = w.Write(resp)
	}
}

// --- Helpers ---

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// ServesConnection reports whether this connection has a matching destination —
// mirroring Deliver's own "no config -> not ours" semantics — so the SDK can
// ack foreign connections before rehydrating large payloads (sdk.ConnectionScoped).
func (p *dbProducer) ServesConnection(ctx context.Context, tenantID, connectionID string) bool {
	if connectionID == "" {
		return false
	}
	targets, err := p.getTargetConnections(ctx, connectionID, tenantID)
	if err != nil {
		// A failed read answers true so Deliver runs and returns it (#704);
		// a missing connection or node is not ours.
		return sdk.IsConfigUnavailable(err)
	}
	return len(targets) > 0
}
