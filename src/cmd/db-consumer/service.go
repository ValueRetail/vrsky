package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"
)

type DBConsumerService struct {
	db     *sql.DB // management DB
	nc     *nats.Conn
	logger *slog.Logger
	config *Config

	activeConnections map[string]*ActiveDBConnection
	mu                sync.RWMutex

	// SSE event subscribers
	eventSubs   map[string][]chan DBEvent
	eventSubsMu sync.RWMutex

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

type DBEvent struct {
	Type    string `json:"type"`              // "connected", "query", "rows", "error", "disconnected"
	Message string `json:"message,omitempty"`
	Count   int    `json:"count,omitempty"`
	Time    string `json:"time"`
}

type ActiveDBConnection struct {
	ConnectionID string
	TenantID     string
	SourceDB     *sql.DB
	Cancel       context.CancelFunc
	DBConfig     SourceDBConfig
}

type SourceDBConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Database string `json:"database"`
	SSLMode  string `json:"sslmode"`
	Query    string `json:"query"`
	Table    string `json:"table"`
	Interval int    `json:"poll_interval_seconds"` // 0 = one-shot
}

func NewDBConsumerService(db *sql.DB, nc *nats.Conn, logger *slog.Logger, config *Config) *DBConsumerService {
	return &DBConsumerService{
		db:                db,
		nc:                nc,
		logger:            logger,
		config:            config,
		activeConnections: make(map[string]*ActiveDBConnection),
		eventSubs:         make(map[string][]chan DBEvent),
	}
}

func (s *DBConsumerService) Start(ctx context.Context) error {
	s.logger.Info("Starting DB Consumer Service")

	startSub, err := s.nc.Subscribe("vrsky.commands.*.connection.start", s.handleStartCommand)
	if err != nil {
		return fmt.Errorf("failed to subscribe to start commands: %w", err)
	}
	s.startSub = startSub

	stopSub, err := s.nc.Subscribe("vrsky.commands.*.connection.stop", s.handleStopCommand)
	if err != nil {
		return fmt.Errorf("failed to subscribe to stop commands: %w", err)
	}
	s.stopSub = stopSub

	s.logger.Info("Subscribed to NATS command topics")
	return nil
}

func (s *DBConsumerService) Stop(ctx context.Context) error {
	s.logger.Info("Stopping DB Consumer Service")

	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}

	s.mu.Lock()
	for connId, ac := range s.activeConnections {
		s.logger.Info("Stopping db consumer", "connection_id", connId)
		ac.Cancel()
		if ac.SourceDB != nil {
			ac.SourceDB.Close()
		}
	}
	s.activeConnections = make(map[string]*ActiveDBConnection)
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return nil
	}
}

type CommandMessage struct {
	ConnectionID string `json:"connection_id"`
	TenantID     string `json:"tenant_id"`
}

func (s *DBConsumerService) handleStartCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse start command", "error", err)
		return
	}

	s.logger.Info("Received start command", "connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)

	s.mu.RLock()
	_, exists := s.activeConnections[cmd.ConnectionID]
	s.mu.RUnlock()
	if exists {
		s.logger.Warn("DB consumer already active", "connection_id", cmd.ConnectionID)
		return
	}

	conn, err := s.getConnection(cmd.ConnectionID, cmd.TenantID)
	if err != nil {
		s.logger.Error("Failed to fetch connection", "error", err)
		return
	}

	dbConfig, ok := s.extractDBConfig(conn)
	if !ok {
		s.logger.Debug("Not a database consumer, ignoring", "connection_id", cmd.ConnectionID)
		return
	}

	// Build connection string for source database
	if dbConfig.Port == 0 {
		dbConfig.Port = 5432
	}
	if dbConfig.SSLMode == "" {
		dbConfig.SSLMode = "disable"
	}

	connStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		dbConfig.Host, dbConfig.Port, dbConfig.User, dbConfig.Password, dbConfig.Database, dbConfig.SSLMode)

	sourceDB, err := sql.Open("postgres", connStr)
	if err != nil {
		s.logger.Error("Failed to open source database", "error", err)
		s.emitEvent(cmd.ConnectionID, DBEvent{
			Type: "error", Message: "Failed to connect: " + err.Error(),
			Time: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	if err := sourceDB.Ping(); err != nil {
		s.logger.Error("Failed to ping source database", "error", err)
		sourceDB.Close()
		s.emitEvent(cmd.ConnectionID, DBEvent{
			Type: "error", Message: "Cannot reach database: " + err.Error(),
			Time: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	ctx, cancel := context.WithCancel(context.Background())

	ac := &ActiveDBConnection{
		ConnectionID: cmd.ConnectionID,
		TenantID:     cmd.TenantID,
		SourceDB:     sourceDB,
		Cancel:       cancel,
		DBConfig:     dbConfig,
	}

	s.mu.Lock()
	s.activeConnections[cmd.ConnectionID] = ac
	s.mu.Unlock()

	if err := s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "running"); err != nil {
		s.logger.Error("Failed to update connection status", "error", err)
	}

	s.emitEvent(cmd.ConnectionID, DBEvent{
		Type: "connected", Message: fmt.Sprintf("Connected to %s@%s:%d/%s", dbConfig.User, dbConfig.Host, dbConfig.Port, dbConfig.Database),
		Time: time.Now().UTC().Format(time.RFC3339),
	})

	go s.runConsumer(ctx, ac)

	s.logger.Info("DB consumer started", "connection_id", cmd.ConnectionID, "host", dbConfig.Host, "database", dbConfig.Database)
}

func (s *DBConsumerService) handleStopCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse stop command", "error", err)
		return
	}

	s.mu.Lock()
	ac, exists := s.activeConnections[cmd.ConnectionID]
	if exists {
		ac.Cancel()
		if ac.SourceDB != nil {
			ac.SourceDB.Close()
		}
		delete(s.activeConnections, cmd.ConnectionID)
	}
	s.mu.Unlock()

	if !exists {
		return
	}

	s.emitEvent(cmd.ConnectionID, DBEvent{
		Type: "disconnected", Message: "Stopped",
		Time: time.Now().UTC().Format(time.RFC3339),
	})

	if err := s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "stopped"); err != nil {
		s.logger.Error("Failed to update connection status", "error", err)
	}
}

func (s *DBConsumerService) runConsumer(ctx context.Context, ac *ActiveDBConnection) {
	logger := s.logger.With("connection_id", ac.ConnectionID)

	query := ac.DBConfig.Query
	if query == "" && ac.DBConfig.Table != "" {
		query = fmt.Sprintf("SELECT * FROM %s", ac.DBConfig.Table)
	}
	if query == "" {
		s.emitEvent(ac.ConnectionID, DBEvent{
			Type: "error", Message: "No query or table specified",
			Time: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	// Run query immediately
	s.executeAndPublish(ctx, ac, query, logger)

	// If polling, repeat on interval
	interval := ac.DBConfig.Interval
	if interval <= 0 {
		return // one-shot
	}

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.executeAndPublish(ctx, ac, query, logger)
		}
	}
}

func (s *DBConsumerService) executeAndPublish(ctx context.Context, ac *ActiveDBConnection, query string, logger *slog.Logger) {
	s.emitEvent(ac.ConnectionID, DBEvent{
		Type: "query", Message: "Executing query...",
		Time: time.Now().UTC().Format(time.RFC3339),
	})

	rows, err := ac.SourceDB.QueryContext(ctx, query)
	if err != nil {
		logger.Error("Query failed", "error", err)
		s.emitEvent(ac.ConnectionID, DBEvent{
			Type: "error", Message: "Query failed: " + err.Error(),
			Time: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		logger.Error("Failed to get columns", "error", err)
		return
	}

	// Collect all rows into a single array
	var allRows []map[string]interface{}
	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			logger.Error("Failed to scan row", "error", err)
			continue
		}

		rowMap := make(map[string]interface{})
		for i, col := range columns {
			val := values[i]
			if b, ok := val.([]byte); ok {
				rowMap[col] = string(b)
			} else {
				rowMap[col] = val
			}
		}
		allRows = append(allRows, rowMap)
	}

	if len(allRows) == 0 {
		logger.Info("Query returned 0 rows")
		s.emitEvent(ac.ConnectionID, DBEvent{
			Type: "rows", Message: "Query returned 0 rows", Count: 0,
			Time: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	// Marshal all rows as a single JSON array
	payload, err := json.MarshalIndent(allRows, "", "  ")
	if err != nil {
		logger.Error("Failed to marshal rows", "error", err)
		return
	}

	// Determine filename from table or query
	filename := ac.DBConfig.Table
	if filename == "" {
		filename = "query_result"
	}
	filename += ".json"

	env := &envelope.Envelope{
		ID:            uuid.New().String(),
		TenantID:      ac.TenantID,
		IntegrationID: ac.ConnectionID,
		Payload:       payload,
		PayloadSize:   int64(len(payload)),
		ContentType:   "application/json",
		Source:        fmt.Sprintf("db:%s/%s", ac.DBConfig.Host, ac.DBConfig.Database),
		CurrentStep:   0,
		StepHistory:   []string{"db-consumer"},
		CreatedAt:     time.Now().UTC(),
		Metadata:      map[string]interface{}{"columns": columns, "row_count": len(allRows), "filename": filename},
	}

	envData, err := json.Marshal(env)
	if err != nil {
		logger.Error("Failed to marshal envelope", "error", err)
		return
	}

	topic := fmt.Sprintf("vrsky.data.%s.pipeline.%s", ac.TenantID, ac.ConnectionID)
	if err := s.nc.Publish(topic, envData); err != nil {
		logger.Error("Failed to publish", "error", err)
		return
	}

	// Cache last payload
	_, _ = s.db.Exec("UPDATE connections SET last_payload = $1 WHERE id = $2", envData, ac.ConnectionID)

	logger.Info("Query executed", "rows", len(allRows))
	s.emitEvent(ac.ConnectionID, DBEvent{
		Type: "rows", Message: fmt.Sprintf("Published %d rows in 1 file", len(allRows)), Count: len(allRows),
		Time: time.Now().UTC().Format(time.RFC3339),
	})
}

// --- Event broadcasting ---

func (s *DBConsumerService) subscribeEvents(connectionID string) (chan DBEvent, func()) {
	ch := make(chan DBEvent, 50)
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

func (s *DBConsumerService) emitEvent(connectionID string, event DBEvent) {
	s.eventSubsMu.RLock()
	defer s.eventSubsMu.RUnlock()
	for _, ch := range s.eventSubs[connectionID] {
		select {
		case ch <- event:
		default:
		}
	}
}

// --- DB helpers ---

type Connection struct {
	ID       string
	TenantID string
	Name     string
	Nodes    json.RawMessage
	Edges    json.RawMessage
}

type Node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

func (s *DBConsumerService) getConnection(connectionID, tenantID string) (*Connection, error) {
	var conn Connection
	err := s.db.QueryRow(
		`SELECT id, tenant_id, name, nodes, edges FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&conn.ID, &conn.TenantID, &conn.Name, &conn.Nodes, &conn.Edges)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("connection not found: %s", connectionID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query connection: %w", err)
	}
	return &conn, nil
}

func (s *DBConsumerService) extractDBConfig(conn *Connection) (SourceDBConfig, bool) {
	var nodes []Node
	if err := json.Unmarshal(conn.Nodes, &nodes); err != nil {
		return SourceDBConfig{}, false
	}
	for _, node := range nodes {
		if node.Type != "consumer" {
			continue
		}
		var config struct {
			Type     string       `json:"type"`
			Database SourceDBConfig `json:"database"`
		}
		if err := json.Unmarshal(node.Config, &config); err != nil {
			continue
		}
		if config.Type == "database" {
			return config.Database, true
		}
	}
	return SourceDBConfig{}, false
}

func (s *DBConsumerService) updateConnectionStatus(connectionID, tenantID, status string) error {
	var query string
	if status == "running" {
		query = `UPDATE connections SET status = $1, started_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	} else {
		query = `UPDATE connections SET status = $1, stopped_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	}
	_, err := s.db.Exec(query, status, connectionID, tenantID)
	return err
}
