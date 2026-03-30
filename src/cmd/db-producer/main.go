package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

type Config struct {
	NATSUrl           string
	DatabaseURL       string // management DB
	LogLevel          string
	SubscriptionTopic string
	Port              string
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

type DBProducerService struct {
	nc     *nats.Conn
	db     *sql.DB // management DB
	logger *slog.Logger
	config *Config

	// Cache target DB connections per connection ID (multiple producers per connection)
	targetCache     map[string][]*TargetConnection
	targetCacheMu   sync.RWMutex
	targetCacheTime map[string]time.Time
	targetCacheTTL  time.Duration

	// SSE events
	eventSubs      map[string][]chan DBProdEvent
	eventSubsMu    sync.RWMutex
	recentEvents   map[string][]DBProdEvent
	recentEventsMu sync.RWMutex

	stopCh    chan struct{}
	stoppedCh chan struct{}
}

type TargetConnection struct {
	DB            *sql.DB
	Config        TargetDBConfig
	PredecessorID string // direct predecessor node ID
	PredIsConsumer bool  // if true, process when _last_processed_by is empty
}

type DBProdEvent struct {
	Type    string `json:"type"`              // "connected", "inserted", "created", "error", "info"
	Message string `json:"message,omitempty"`
	Count   int    `json:"count,omitempty"`
	Time    string `json:"time"`
	Payload string `json:"payload,omitempty"` // data preview (truncated)
	Table   string `json:"table,omitempty"`
	Columns []string `json:"columns,omitempty"`
}

func main() {
	logLevel := os.Getenv("LOG_LEVEL")
	var level slog.Level
	switch strings.ToLower(logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	config := &Config{
		NATSUrl:           getEnv("NATS_URL", "nats://localhost:4222"),
		DatabaseURL:       getEnv("DATABASE_URL", ""),
		LogLevel:          logLevel,
		SubscriptionTopic: getEnv("NATS_SUBSCRIPTION_TOPIC", "vrsky.data.*.pipeline.*"),
		Port:              getEnv("DB_PRODUCER_PORT", "9500"),
	}

	if config.DatabaseURL == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	logger.Info("Starting DB Producer Service", "version", "1.0.0")

	db, err := sql.Open("postgres", config.DatabaseURL)
	if err != nil {
		logger.Error("Failed to open management database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		logger.Error("Failed to ping management database", "error", err)
		os.Exit(1)
	}
	logger.Info("Management database connected")

	nc, err := initNATS(config.NATSUrl, logger)
	if err != nil {
		logger.Error("Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	service := &DBProducerService{
		nc:              nc,
		db:              db,
		logger:          logger,
		config:          config,
		targetCache:     make(map[string][]*TargetConnection),
		targetCacheTime: make(map[string]time.Time),
		targetCacheTTL:  5 * time.Minute,
		eventSubs:       make(map[string][]chan DBProdEvent),
		recentEvents:    make(map[string][]DBProdEvent),
		stopCh:          make(chan struct{}),
		stoppedCh:       make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := service.Start(ctx); err != nil {
		logger.Error("Failed to start service", "error", err)
		os.Exit(1)
	}

	startHTTPServer(config.Port, service, logger)

	logger.Info("DB Producer Service running. Press Ctrl+C to stop.")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	logger.Info("Shutting down...")
	cancel()
	service.Stop()
}

func (s *DBProducerService) Start(ctx context.Context) error {
	sub, err := s.nc.Subscribe(s.config.SubscriptionTopic, func(msg *nats.Msg) {
		s.handleMessage(ctx, msg)
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe: %w", err)
	}
	s.logger.Info("Subscribed to NATS", "topic", s.config.SubscriptionTopic)

	go func() {
		<-s.stopCh
		_ = sub.Unsubscribe()
		// Close all target connections
		s.targetCacheMu.Lock()
		for _, tcs := range s.targetCache {
			for _, tc := range tcs {
				tc.DB.Close()
			}
		}
		s.targetCacheMu.Unlock()
		close(s.stoppedCh)
	}()
	return nil
}

func (s *DBProducerService) Stop() {
	close(s.stopCh)
	<-s.stoppedCh
}

func (s *DBProducerService) handleMessage(ctx context.Context, msg *nats.Msg) {
	var env envelope.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		s.logger.Error("Failed to unmarshal envelope", "error", err)
		return
	}

	connectionID := env.IntegrationID
	if connectionID == "" {
		parts := strings.Split(msg.Subject, ".")
		if len(parts) >= 5 {
			connectionID = parts[4]
		}
	}
	if connectionID == "" {
		return
	}

	tcs, err := s.getTargetConnections(ctx, connectionID)
	if err != nil {
		s.logger.Debug("No DB producer config", "connection_id", connectionID, "error", err)
		return
	}

	// Predecessor-based routing
	lastProcessedBy := ""
	if env.Metadata != nil {
		if v, ok := env.Metadata["_last_processed_by"].(string); ok {
			lastProcessedBy = v
		}
	}

	for _, tc := range tcs {
		if tc.PredIsConsumer && lastProcessedBy != "" {
			continue
		}
		if !tc.PredIsConsumer && tc.PredecessorID != "" && lastProcessedBy != tc.PredecessorID {
			continue
		}

		s.processForTarget(connectionID, tc, &env)
	}
}

func (s *DBProducerService) processForTarget(connectionID string, tc *TargetConnection, env *envelope.Envelope) {
	// Parse payload — could be a single object or an array of objects
	var rows []map[string]interface{}
	payload := bytes.TrimSpace(env.Payload)
	if len(payload) > 0 && payload[0] == '[' {
		if err := json.Unmarshal(payload, &rows); err != nil {
			s.emitEvent(connectionID, DBProdEvent{Type: "error", Message: "Failed to parse JSON array: " + err.Error(), Time: now()})
			return
		}
	} else if len(payload) > 0 && payload[0] == '{' {
		var single map[string]interface{}
		if err := json.Unmarshal(payload, &single); err != nil {
			s.emitEvent(connectionID, DBProdEvent{Type: "error", Message: "Failed to parse JSON: " + err.Error(), Time: now()})
			return
		}
		rows = []map[string]interface{}{single}
	} else {
		s.emitEvent(connectionID, DBProdEvent{Type: "error", Message: "Payload is not JSON", Time: now()})
		return
	}

	if len(rows) == 0 {
		return
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

	s.emitEvent(connectionID, DBProdEvent{Type: "info", Message: fmt.Sprintf("Writing %d rows to %s", len(rows), table), Time: now(), Payload: payloadPreview, Table: table, Columns: columns})

	if mode == "create_insert" {
		if err := s.ensureTable(tc, table, rows[0]); err != nil {
			s.emitEvent(connectionID, DBProdEvent{Type: "error", Message: "Failed to create table: " + err.Error(), Time: now()})
			return
		}
	}

	inserted := 0
	for _, row := range rows {
		if err := s.insertRow(tc, table, row); err != nil {
			s.logger.Error("Failed to insert row", "error", err, "table", table)
			s.emitEvent(connectionID, DBProdEvent{Type: "error", Message: "Insert failed: " + err.Error(), Time: now()})
			continue
		}
		inserted++
	}

	s.logger.Info("Rows inserted", "connection_id", connectionID, "table", table, "count", inserted)
	s.emitEvent(connectionID, DBProdEvent{
		Type: "inserted", Message: fmt.Sprintf("Inserted %d/%d rows into %s", inserted, len(rows), table),
		Count: inserted, Time: now(), Payload: payloadPreview, Table: table, Columns: columns,
	})
}

func (s *DBProducerService) ensureTable(tc *TargetConnection, table string, sample map[string]interface{}) error {
	// Check if table exists
	var exists bool
	err := tc.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name=$1)", table).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	// Build CREATE TABLE from sample keys
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
	_, err = tc.DB.Exec(query)
	if err != nil {
		return err
	}

	s.logger.Info("Created table", "table", table, "columns", len(cols))
	return nil
}

func (s *DBProducerService) insertRow(tc *TargetConnection, table string, row map[string]interface{}) error {
	columns := make([]string, 0, len(row))
	placeholders := make([]string, 0, len(row))
	values := make([]interface{}, 0, len(row))

	i := 1
	for key, val := range row {
		columns = append(columns, quoteIdent(key))
		placeholders = append(placeholders, fmt.Sprintf("$%d", i))
		// Convert nested objects to JSON strings
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

func (s *DBProducerService) getTargetConnections(ctx context.Context, connectionID string) ([]*TargetConnection, error) {
	s.targetCacheMu.RLock()
	if tcs, ok := s.targetCache[connectionID]; ok {
		if time.Since(s.targetCacheTime[connectionID]) < s.targetCacheTTL {
			s.targetCacheMu.RUnlock()
			return tcs, nil
		}
	}
	s.targetCacheMu.RUnlock()

	var nodesJSON, edgesJSON []byte
	err := s.db.QueryRowContext(ctx, `SELECT nodes, edges FROM connections WHERE id = $1`, connectionID).Scan(&nodesJSON, &edgesJSON)
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

		targetDB, err := sql.Open("postgres", connStr)
		if err != nil {
			s.logger.Error("Failed to open target DB", "error", err)
			continue
		}
		if err := targetDB.Ping(); err != nil {
			targetDB.Close()
			s.logger.Error("Failed to ping target DB", "error", err)
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

	s.targetCacheMu.Lock()
	s.targetCache[connectionID] = tcs
	s.targetCacheTime[connectionID] = time.Now()
	s.targetCacheMu.Unlock()

	return tcs, nil
}

// --- Events ---

func (s *DBProducerService) subscribeEvents(connectionID string) (chan DBProdEvent, func()) {
	ch := make(chan DBProdEvent, 50)
	s.eventSubsMu.Lock()
	s.eventSubs[connectionID] = append(s.eventSubs[connectionID], ch)
	s.eventSubsMu.Unlock()
	return ch, func() {
		s.eventSubsMu.Lock()
		defer s.eventSubsMu.Unlock()
		subs := s.eventSubs[connectionID]
		for i, sub := range subs {
			if sub == ch {
				s.eventSubs[connectionID] = append(subs[:i], subs[i+1:]...)
				close(ch)
				break
			}
		}
	}
}

func (s *DBProducerService) emitEvent(connectionID string, event DBProdEvent) {
	s.recentEventsMu.Lock()
	s.recentEvents[connectionID] = append(s.recentEvents[connectionID], event)
	if len(s.recentEvents[connectionID]) > 50 {
		s.recentEvents[connectionID] = s.recentEvents[connectionID][len(s.recentEvents[connectionID])-50:]
	}
	s.recentEventsMu.Unlock()

	s.eventSubsMu.RLock()
	defer s.eventSubsMu.RUnlock()
	for _, ch := range s.eventSubs[connectionID] {
		select {
		case ch <- event:
		default:
		}
	}
}

func (s *DBProducerService) getRecentEvents(connectionID string) []DBProdEvent {
	s.recentEventsMu.RLock()
	defer s.recentEventsMu.RUnlock()
	cp := make([]DBProdEvent, len(s.recentEvents[connectionID]))
	copy(cp, s.recentEvents[connectionID])
	return cp
}

// --- HTTP server ---

func startHTTPServer(port string, service *DBProducerService, logger *slog.Logger) {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("/test-connection/", func(w http.ResponseWriter, r *http.Request) {
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
		// List tables
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
	})

	mux.HandleFunc("/events/", func(w http.ResponseWriter, r *http.Request) {
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

		ch, unsub := service.subscribeEvents(connectionID)
		defer unsub()

		fmt.Fprintf(w, "data: {\"type\":\"connected\",\"message\":\"Listening for DB producer events\"}\n\n")
		for _, event := range service.getRecentEvents(connectionID) {
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
	})

	server := &http.Server{Addr: ":" + port, Handler: mux}
	go func() {
		logger.Info("DB Producer HTTP server started", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error", "error", err)
		}
	}()
}

// --- Helpers ---

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func initNATS(natsURL string, logger *slog.Logger) (*nats.Conn, error) {
	opts := []nats.Option{
		nats.Name("VRSky-DB-Producer"),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) { logger.Warn("NATS disconnected", "error", err) }),
		nats.ReconnectHandler(func(nc *nats.Conn) { logger.Info("NATS reconnected") }),
	}
	return nats.Connect(natsURL, opts...)
}
