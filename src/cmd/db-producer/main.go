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

type TargetConnection struct {
	DB             *sql.DB
	Config         TargetDBConfig
	PredecessorID  string // direct predecessor node ID
	PredIsConsumer bool   // if true, process when _last_processed_by is empty
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
			if tc.DB != nil {
				_ = tc.DB.Close()
			}
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
			if tc.DB != nil {
				_ = tc.DB.Close()
			}
		}
	}
	p.targetCache = make(map[string][]*TargetConnection)
	return nil
}

// Deliver writes one envelope into every matching target database configured
// for its connection. Transient DB failures (open/ping/exec) return
// sdk.Retriable so the SDK NAKs → retries → DLQs; a malformed (non-JSON)
// payload returns sdk.Permanent (retrying can't help). A missing producer
// config for the connection is not an error — this binary just isn't the
// producer for it.
func (p *dbProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	connectionID := env.IntegrationID
	if connectionID == "" {
		return nil
	}

	tcs, err := p.getTargetConnections(ctx, connectionID, env.TenantID)
	if err != nil {
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

	var transient error
	for _, tc := range tcs {
		if tc.PredIsConsumer && lastProcessedBy != "" {
			continue
		}
		if !tc.PredIsConsumer && tc.PredecessorID != "" && lastProcessedBy != tc.PredecessorID {
			continue
		}

		if err := p.processForTarget(connectionID, tc, env); err != nil {
			if errors.Is(err, errBadPayload) {
				// Malformed payload — drop (Permanent); retrying can't help.
				p.logger.Error("dropping: payload is not valid JSON", "error", err, "envelope_id", env.ID)
				return sdk.Permanent(err)
			}
			if transient == nil {
				transient = err
			}
		}
	}
	if transient != nil {
		return sdk.Retriable(transient)
	}
	return nil
}

// errBadPayload marks an envelope whose payload isn't JSON — a poison message.
var errBadPayload = errors.New("payload is not valid JSON")

// processForTarget writes one envelope to one target DB. It returns
// errBadPayload for unparseable payloads (Permanent) and a transient error if
// table creation fails (Retriable); per-row insert failures are logged and the
// batch continues (best-effort, as before).
func (p *dbProducer) processForTarget(connectionID string, tc *TargetConnection, env *envelope.Envelope) error {
	// Parse payload — a single object or an array of objects.
	var rows []map[string]interface{}
	payload := bytes.TrimSpace(env.Payload)
	if len(payload) > 0 && payload[0] == '[' {
		if err := json.Unmarshal(payload, &rows); err != nil {
			p.emitEvent(connectionID, DBProdEvent{Type: "error", Message: "Failed to parse JSON array: " + err.Error(), Time: now()})
			return fmt.Errorf("%w: %v", errBadPayload, err)
		}
	} else if len(payload) > 0 && payload[0] == '{' {
		var single map[string]interface{}
		if err := json.Unmarshal(payload, &single); err != nil {
			p.emitEvent(connectionID, DBProdEvent{Type: "error", Message: "Failed to parse JSON: " + err.Error(), Time: now()})
			return fmt.Errorf("%w: %v", errBadPayload, err)
		}
		rows = []map[string]interface{}{single}
	} else {
		p.emitEvent(connectionID, DBProdEvent{Type: "error", Message: "Payload is not JSON", Time: now()})
		return errBadPayload
	}

	if len(rows) == 0 {
		return nil
	}

	table := tc.Config.Table
	mode := tc.Config.Mode
	if mode == "" {
		mode = "create_insert"
	}

	payloadPreview := string(payload)
	if len(payloadPreview) > 3000 {
		payloadPreview = payloadPreview[:3000] + "..."
	}
	var columns []string
	for k := range rows[0] {
		columns = append(columns, k)
	}

	p.emitEvent(connectionID, DBProdEvent{Type: "info", Message: fmt.Sprintf("Writing %d rows to %s", len(rows), table), Time: now(), Payload: payloadPreview, Table: table, Columns: columns})

	if mode == "create_insert" {
		if err := p.ensureTable(tc, table, rows[0]); err != nil {
			p.emitEvent(connectionID, DBProdEvent{Type: "error", Message: "Failed to create table: " + err.Error(), Time: now()})
			return fmt.Errorf("ensure table %q: %w", table, err)
		}
	}

	inserted := 0
	for _, row := range rows {
		if err := p.insertRow(tc, table, row); err != nil {
			p.logger.Error("Failed to insert row", "error", err, "table", table)
			p.emitEvent(connectionID, DBProdEvent{Type: "error", Message: "Insert failed: " + err.Error(), Time: now()})
			continue
		}
		inserted++
	}

	p.logger.Info("Rows inserted", "connection_id", connectionID, "table", table, "count", inserted)
	p.emitEvent(connectionID, DBProdEvent{
		Type: "inserted", Message: fmt.Sprintf("Inserted %d/%d rows into %s", inserted, len(rows), table),
		Count: inserted, Time: now(), Payload: payloadPreview, Table: table, Columns: columns,
	})
	return nil
}

func (p *dbProducer) ensureTable(tc *TargetConnection, table string, sample map[string]interface{}) error {
	var exists bool
	err := tc.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name=$1)", table).Scan(&exists)
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

	query := fmt.Sprintf("CREATE TABLE %s (%s)", quoteIdent(table), strings.Join(cols, ", "))
	if _, err = tc.DB.Exec(query); err != nil {
		return err
	}

	p.logger.Info("Created table", "table", table, "columns", len(cols))
	return nil
}

func (p *dbProducer) insertRow(tc *TargetConnection, table string, row map[string]interface{}) error {
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
		quoteIdent(table),
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "))

	_, err := tc.DB.Exec(query, values...)
	return err
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
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
		return nil, fmt.Errorf("connection not found: %w", err)
	}

	var nodes []struct {
		ID     string          `json:"id"`
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, fmt.Errorf("failed to parse nodes: %w", err)
	}

	// Decrypt any *_secret_id references in node configs (the UI stores the
	// target DB password as password_secret_id) so the typed TargetDBConfig
	// below sees plaintext. Fatal on error — better to fail loud than dial
	// with an empty password.
	reader := crypto.NewSQLSecretReader(p.db)
	for i := range nodes {
		resolved, rerr := crypto.ResolveSecretsInJSON(ctx, reader, tenantID, nodes[i].Config)
		if rerr != nil {
			return nil, fmt.Errorf("resolve secrets for node %s: %w", nodes[i].ID, rerr)
		}
		nodes[i].Config = resolved
	}

	var edges []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if edgesJSON != nil {
		_ = json.Unmarshal(edgesJSON, &edges)
	}

	var tcs []*TargetConnection
	for _, node := range nodes {
		if node.Type != "producer" {
			continue
		}
		var nodeConfig struct {
			Type     string         `json:"type"`
			Database TargetDBConfig `json:"database"`
		}
		if err := json.Unmarshal(node.Config, &nodeConfig); err != nil {
			continue
		}
		if nodeConfig.Type != "database" || nodeConfig.Database.Host == "" {
			continue
		}

		cfg := nodeConfig.Database
		if cfg.Port == 0 {
			cfg.Port = 5432
		}
		if cfg.SSLMode == "" {
			cfg.SSLMode = "disable"
		}

		connStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
			cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Database, cfg.SSLMode)

		targetDB, err := p.openTarget(connStr)
		if err != nil {
			p.logger.Error("Failed to open target DB", "error", err)
			continue
		}
		if err := targetDB.Ping(); err != nil {
			targetDB.Close()
			p.logger.Error("Failed to ping target DB", "error", err)
			continue
		}

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

		tcs = append(tcs, &TargetConnection{DB: targetDB, Config: cfg, PredecessorID: predID, PredIsConsumer: predIsConsumer})
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
			if tc.DB != nil {
				_ = tc.DB.Close()
			}
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
		connStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s connect_timeout=5",
			cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Database, cfg.SSLMode)
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
