package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	iolib "io"
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
	"github.com/ValueRetail/vrsky/pkg/messaging"
)

type Config struct {
	NATSUrl           string
	DatabaseURL       string
	LogLevel          string
	SubscriptionTopic string
	Port              string
}

type HTTPProducerService struct {
	nc     *nats.Conn
	js     nats.JetStreamContext
	sub    *messaging.Subscriber // JetStream subscriber (#70)
	db     *sql.DB
	logger *slog.Logger
	config *Config

	// Cache for connection HTTP configs (multiple producer nodes per connection)
	configCache     map[string][]*HTTPConfig
	configCacheMu   sync.RWMutex
	configCacheTime map[string]time.Time
	configCacheTTL  time.Duration

	// SSE event subscribers
	eventSubs   map[string][]chan HTTPEvent
	eventSubsMu sync.RWMutex

	// Recent event buffer for replay on SSE connect
	recentEvents   map[string][]HTTPEvent
	recentEventsMu sync.RWMutex

	stopCh    chan struct{}
	stoppedCh chan struct{}
}

type HTTPEvent struct {
	Type       string `json:"type"`                  // "sent", "error", "info"
	Message    string `json:"message,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	Time       string `json:"time"`
	Payload    string `json:"payload,omitempty"`     // request body (truncated)
	Response   string `json:"response,omitempty"`    // response body (truncated)
}

func main() {
	logLevel := os.Getenv("LOG_LEVEL")
	var level slog.Level
	switch strings.ToLower(logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
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
		Port:              getEnv("HTTP_PRODUCER_PORT", "9400"),
	}

	if config.DatabaseURL == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	logger.Info("Starting HTTP Producer Service", "version", "1.0.0")

	db, err := sql.Open("postgres", config.DatabaseURL)
	if err != nil {
		logger.Error("Failed to open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		logger.Error("Failed to ping database", "error", err)
		os.Exit(1)
	}
	logger.Info("Database connected")

	nc, err := initNATS(config.NATSUrl, logger)
	if err != nil {
		logger.Error("Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	js, jsErr := nc.JetStream()
	if jsErr != nil {
		logger.Error("Failed to get JetStream context", "error", jsErr)
		os.Exit(1)
	}
	service := &HTTPProducerService{
		nc:              nc,
		js:              js,
		db:              db,
		logger:          logger,
		config:          config,
		configCache:     make(map[string][]*HTTPConfig),
		configCacheTime: make(map[string]time.Time),
		configCacheTTL:  5 * time.Minute,
		eventSubs:       make(map[string][]chan HTTPEvent),
		recentEvents:    make(map[string][]HTTPEvent),
		stopCh:          make(chan struct{}),
		stoppedCh:       make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := service.Start(ctx); err != nil {
		logger.Error("Failed to start service", "error", err)
		os.Exit(1)
	}

	// Start SSE/health HTTP server
	startHTTPServer(config.Port, service, logger)

	logger.Info("HTTP Producer Service running. Press Ctrl+C to stop.")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	logger.Info("Shutting down...")
	cancel()
	service.Stop()
	logger.Info("HTTP Producer Service stopped")
}

func (s *HTTPProducerService) Start(ctx context.Context) error {
	sub, err := messaging.Subscribe(s.js, messaging.SubscriberOpts{
		DurableName: "http-producer",
		AckWait:     45 * time.Second,
		Logger:      s.logger,
	}, func(ctx context.Context, msg *nats.Msg) error {
		return s.handleMessage(ctx, msg)
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe via JetStream: %w", err)
	}
	s.sub = sub
	s.logger.Info("Subscribed via JetStream", "durable", "http-producer")

	go func() {
		<-s.stopCh
		s.sub.Stop()
		close(s.stoppedCh)
	}()

	return nil
}

type HTTPConfig struct {
	URL            string            `json:"url"`
	Method         string            `json:"method"`
	Headers        map[string]string `json:"headers"`
	PredecessorID  string
	PredIsConsumer bool
}

func (s *HTTPProducerService) Stop() {
	close(s.stopCh)
	<-s.stoppedCh
}

// handleMessage processes one envelope. Returning nil acks the message.
// A non-nil error triggers JS redelivery with backoff; after MaxDeliver
// the message moves to the DLQ stream.
//
// Decoding/config errors are NOT retried — the underlying state cannot
// improve through a redelivery. Network/HTTP errors ARE retried because
// the remote endpoint may recover.
func (s *HTTPProducerService) handleMessage(ctx context.Context, msg *nats.Msg) error {
	var env envelope.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		s.logger.Error("Failed to unmarshal envelope; dropping", "error", err)
		return nil // unrecoverable
	}

	connectionID := env.IntegrationID
	if connectionID == "" {
		parts := strings.Split(msg.Subject, ".")
		if len(parts) >= 5 {
			connectionID = parts[4]
		}
	}
	if connectionID == "" {
		s.logger.Error("No connection ID; dropping", "envelope_id", env.ID)
		return nil
	}

	httpConfigs, err := s.getHTTPConfigs(ctx, connectionID)
	if err != nil {
		// Not an http producer for this pipeline — ack and move on.
		s.logger.Debug("No HTTP producer config for connection", "connection_id", connectionID, "error", err)
		return nil
	}

	lastProcessedBy := ""
	if env.Metadata != nil {
		if v, ok := env.Metadata["_last_processed_by"].(string); ok {
			lastProcessedBy = v
		}
	}

	var firstErr error
	for _, httpCfg := range httpConfigs {
		if httpCfg.PredIsConsumer && lastProcessedBy != "" {
			continue
		}
		if !httpCfg.PredIsConsumer && httpCfg.PredecessorID != "" && lastProcessedBy != httpCfg.PredecessorID {
			continue
		}
		if err := s.sendHTTPRequest(ctx, connectionID, httpCfg, &env); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// sendHTTPRequest returns nil on a successful 2xx response, or a non-nil
// error on any *retriable* failure (network error, 5xx). 4xx responses are
// returned as nil so JS doesn't retry them — the request is malformed and
// will keep failing on every redelivery.
func (s *HTTPProducerService) sendHTTPRequest(ctx context.Context, connectionID string, httpCfg *HTTPConfig, env *envelope.Envelope) error {
	payloadPreview := string(env.Payload)
	if len(payloadPreview) > 2000 {
		payloadPreview = payloadPreview[:2000] + "..."
	}

	s.emitEvent(connectionID, HTTPEvent{
		Type:    "info",
		Message: fmt.Sprintf("Sending %d bytes to %s", len(env.Payload), httpCfg.URL),
		Payload: payloadPreview,
		Time:    time.Now().UTC().Format(time.RFC3339),
	})

	method := httpCfg.Method
	if method == "" {
		method = "POST"
	}

	req, err := http.NewRequestWithContext(ctx, method, httpCfg.URL, bytes.NewReader(env.Payload))
	if err != nil {
		s.emitEvent(connectionID, HTTPEvent{
			Type: "error", Message: "Failed to create request: " + err.Error(),
			Time: time.Now().UTC().Format(time.RFC3339),
		})
		return nil // bad URL — won't improve on retry
	}

	if env.ContentType != "" {
		req.Header.Set("Content-Type", env.ContentType)
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Message-ID", env.ID)
	for k, v := range httpCfg.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.logger.Error("HTTP request failed", "error", err, "connection_id", connectionID)
		s.emitEvent(connectionID, HTTPEvent{
			Type: "error", Message: err.Error(),
			Time: time.Now().UTC().Format(time.RFC3339),
		})
		return fmt.Errorf("transport: %w", err) // retriable
	}
	defer resp.Body.Close()

	respBody, _ := iolib.ReadAll(iolib.LimitReader(resp.Body, 4096))
	respPreview := string(respBody)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		s.logger.Info("HTTP request sent", "connection_id", connectionID, "status", resp.StatusCode, "size", len(env.Payload))
		s.emitEvent(connectionID, HTTPEvent{
			Type: "sent", Message: fmt.Sprintf("%s %s → %d", method, httpCfg.URL, resp.StatusCode),
			StatusCode: resp.StatusCode, Payload: payloadPreview, Response: respPreview,
			Time: time.Now().UTC().Format(time.RFC3339),
		})
		return nil
	}

	s.logger.Error("HTTP request returned error", "status", resp.StatusCode, "connection_id", connectionID)
	s.emitEvent(connectionID, HTTPEvent{
		Type: "error", Message: fmt.Sprintf("%s %s → %d", method, httpCfg.URL, resp.StatusCode),
		StatusCode: resp.StatusCode, Response: respPreview,
		Time: time.Now().UTC().Format(time.RFC3339),
	})
	if resp.StatusCode >= 500 || resp.StatusCode == 429 {
		return fmt.Errorf("upstream %d", resp.StatusCode) // retriable
	}
	return nil // 4xx — don't retry
}

func (s *HTTPProducerService) getHTTPConfigs(ctx context.Context, connectionID string) ([]*HTTPConfig, error) {
	// Check cache
	s.configCacheMu.RLock()
	if cfg, ok := s.configCache[connectionID]; ok {
		if time.Since(s.configCacheTime[connectionID]) < s.configCacheTTL {
			s.configCacheMu.RUnlock()
			return cfg, nil
		}
	}
	s.configCacheMu.RUnlock()

	// Query DB for connection config
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

	var configs []*HTTPConfig
	for _, node := range nodes {
		if node.Type != "producer" {
			continue
		}

		var nodeConfig struct {
			Type string `json:"type"`
			HTTP struct {
				URL     string            `json:"url"`
				Method  string            `json:"method"`
				Headers map[string]string `json:"headers"`
			} `json:"http"`
		}
		if err := json.Unmarshal(node.Config, &nodeConfig); err != nil {
			continue
		}
		if nodeConfig.Type != "http" || nodeConfig.HTTP.URL == "" {
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

		configs = append(configs, &HTTPConfig{
			URL:            nodeConfig.HTTP.URL,
			Method:         nodeConfig.HTTP.Method,
			Headers:        nodeConfig.HTTP.Headers,
			PredecessorID:  predID,
			PredIsConsumer: predIsConsumer,
		})
	}

	if len(configs) == 0 {
		return nil, fmt.Errorf("no HTTP producer config found")
	}

	s.configCacheMu.Lock()
	s.configCache[connectionID] = configs
	s.configCacheTime[connectionID] = time.Now()
	s.configCacheMu.Unlock()

	return configs, nil
}

// --- Event broadcasting ---

func (s *HTTPProducerService) subscribeEvents(connectionID string) (chan HTTPEvent, func()) {
	ch := make(chan HTTPEvent, 50)
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

func (s *HTTPProducerService) emitEvent(connectionID string, event HTTPEvent) {
	// Buffer recent events for replay
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

func (s *HTTPProducerService) getRecentEvents(connectionID string) []HTTPEvent {
	s.recentEventsMu.RLock()
	defer s.recentEventsMu.RUnlock()
	events := s.recentEvents[connectionID]
	cp := make([]HTTPEvent, len(events))
	copy(cp, events)
	return cp
}

// --- HTTP server for SSE + health ---

func startHTTPServer(port string, service *HTTPProducerService, logger *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
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

		fmt.Fprintf(w, "data: {\"type\":\"connected\",\"message\":\"Listening for HTTP producer events\"}\n\n")
		flusher.Flush()

		// Replay recent events so client catches up
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

	server := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	go func() {
		logger.Info("HTTP Producer server started", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error", "error", err)
		}
	}()
}

// --- Helpers ---

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func initNATS(natsURL string, logger *slog.Logger) (*nats.Conn, error) {
	opts := []nats.Option{
		nats.Name("VRSky-HTTP-Producer"),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			logger.Warn("NATS disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Info("NATS reconnected", "url", nc.ConnectedUrl())
		}),
	}

	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	logger.Info("NATS connected", "url", nc.ConnectedUrl())
	return nc, nil
}
