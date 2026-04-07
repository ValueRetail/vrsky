package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
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
	DatabaseURL       string
	LogLevel          string
	SubscriptionTopic string
	Port              string
}

type FilterRule struct {
	Field    string `json:"field"`
	Operator string `json:"operator"` // "equals", "not_equals", "contains", "not_contains", "gt", "lt", "gte", "lte", "is_empty", "is_not_empty", "regex"
	Value    string `json:"value"`
}

type FilterNodeConfig struct {
	Rules         []FilterRule `json:"rules"`
	Logic         string       `json:"logic"`          // "and", "or" (default "and")
	ExtractFields []string     `json:"extract_fields"` // JSON paths to keep

	// Flatten: unroll a nested array into flat rows
	FlattenPath    string            `json:"flatten_path"`    // Path to array to unroll, e.g. "properties.timeseries"
	FlattenFields  map[string]string `json:"flatten_fields"`  // Element path → output name, e.g. {"data.instant.details.air_temperature": "air_temperature"}
	FlattenInclude map[string]string `json:"flatten_include"` // Parent path → output name, e.g. {"geometry.coordinates[0]": "lon"}
}

type FilterEntry struct {
	NodeID         string
	Config         *FilterNodeConfig
	PredecessorID  string
	PredIsConsumer bool
}

type PipelineInfo struct {
	Entries []*FilterEntry
}

type FilterService struct {
	nc     *nats.Conn
	db     *sql.DB
	logger *slog.Logger
	config *Config

	pipelineCache     map[string]*PipelineInfo
	pipelineCacheMu   sync.RWMutex
	pipelineCacheTime map[string]time.Time
	pipelineCacheTTL  time.Duration

	eventSubs      map[string][]chan FilterEvent
	eventSubsMu    sync.RWMutex
	recentEvents   map[string][]FilterEvent
	recentEventsMu sync.RWMutex

	stopCh    chan struct{}
	stoppedCh chan struct{}
}

type FilterEvent struct {
	Type    string `json:"type"`              // "passed", "dropped", "error", "info", "connected"
	Message string `json:"message,omitempty"`
	Time    string `json:"time"`
	Data    string `json:"data,omitempty"` // row that was evaluated
	Rules   int    `json:"rules,omitempty"`
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
		Port:              getEnv("FILTER_PORT", "9700"),
	}

	if config.DatabaseURL == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	logger.Info("Starting Data Filter Service", "version", "1.0.0")

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

	nc, err := initNATS(config.NATSUrl, logger)
	if err != nil {
		logger.Error("Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	service := &FilterService{
		nc:                nc,
		db:                db,
		logger:            logger,
		config:            config,
		pipelineCache:     make(map[string]*PipelineInfo),
		pipelineCacheTime: make(map[string]time.Time),
		pipelineCacheTTL:  5 * time.Minute,
		eventSubs:         make(map[string][]chan FilterEvent),
		recentEvents:      make(map[string][]FilterEvent),
		stopCh:            make(chan struct{}),
		stoppedCh:         make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := service.Start(ctx); err != nil {
		logger.Error("Failed to start service", "error", err)
		os.Exit(1)
	}

	startHTTPServer(config.Port, service, logger)

	logger.Info("Data Filter Service running. Press Ctrl+C to stop.")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	cancel()
	service.Stop()
}

func (s *FilterService) Start(ctx context.Context) error {
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
		close(s.stoppedCh)
	}()
	return nil
}

func (s *FilterService) Stop() {
	close(s.stopCh)
	<-s.stoppedCh
}

func (s *FilterService) handleMessage(ctx context.Context, msg *nats.Msg) {
	var env envelope.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
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

	info, err := s.getPipelineInfo(ctx, connectionID)
	if err != nil || info == nil || len(info.Entries) == 0 {
		return
	}

	// Find which filter entry should handle this message based on predecessor
	lastProcessedBy := ""
	if env.Metadata != nil {
		if v, ok := env.Metadata["_last_processed_by"].(string); ok {
			lastProcessedBy = v
		}
	}

	// Process ALL matching entries for branching pipelines
	for _, entry := range info.Entries {
		if entry.PredIsConsumer && lastProcessedBy != "" {
			continue
		}
		if !entry.PredIsConsumer && entry.PredecessorID != "" && lastProcessedBy != entry.PredecessorID {
			continue
		}

		s.processFilterEntry(connectionID, msg.Subject, &env, entry)
	}
}

func (s *FilterService) processFilterEntry(connectionID, subject string, origEnv *envelope.Envelope, entry *FilterEntry) {
	var data interface{}
	if err := json.Unmarshal(origEnv.Payload, &data); err != nil {
		s.emitEvent(connectionID, FilterEvent{Type: "error", Message: "Invalid JSON payload", Time: now()})
		return
	}

	hasRules := len(entry.Config.Rules) > 0
	hasExtract := len(entry.Config.ExtractFields) > 0

	// Step 1: Row filtering (if rules are configured)
	var filtered interface{} = data
	var passed, dropped int
	if hasRules {
		logic := entry.Config.Logic
		if logic == "" {
			logic = "and"
		}

		switch d := data.(type) {
		case []interface{}:
			var result []interface{}
			for _, item := range d {
				if obj, ok := item.(map[string]interface{}); ok {
					if evaluateRules(obj, entry.Config.Rules, logic) {
						result = append(result, obj)
						passed++
					} else {
						dropped++
					}
				}
			}
			if len(result) == 0 {
				s.emitEvent(connectionID, FilterEvent{
					Type: "dropped", Message: fmt.Sprintf("All %d rows filtered out", dropped),
					Time: now(), Rules: len(entry.Config.Rules),
				})
				return
			}
			filtered = result
		case map[string]interface{}:
			if evaluateRules(d, entry.Config.Rules, logic) {
				filtered = d
				passed = 1
			} else {
				s.emitEvent(connectionID, FilterEvent{
					Type: "dropped", Message: "Message filtered out",
					Time: now(), Rules: len(entry.Config.Rules),
				})
				return
			}
		default:
			filtered = data
			passed = 1
		}
	} else {
		passed = 1
	}

	// Step 2: Field extraction (if extract_fields is configured)
	if hasExtract {
		filtered = extractFields(filtered, entry.Config.ExtractFields)
	}

	// Step 3: Flatten array into rows (if flatten_path is configured)
	if entry.Config.FlattenPath != "" {
		filtered = flattenData(filtered, entry.Config)
	}

	newPayload, err := json.Marshal(filtered)
	if err != nil {
		return
	}

	// Build a new envelope copy for this branch
	env := *origEnv
	env.Payload = newPayload
	env.Metadata = make(map[string]interface{})
	for k, v := range origEnv.Metadata {
		env.Metadata[k] = v
	}
	env.Metadata["_last_processed_by"] = entry.NodeID

	envData, _ := json.Marshal(env)
	if err := s.nc.Publish(subject, envData); err != nil {
		s.emitEvent(connectionID, FilterEvent{Type: "error", Message: "Failed to re-publish: " + err.Error(), Time: now()})
		return
	}

	msgParts := []string{}
	if hasRules {
		msgParts = append(msgParts, fmt.Sprintf("Passed %d, dropped %d rows (%d rules)", passed, dropped, len(entry.Config.Rules)))
	}
	if hasExtract {
		msgParts = append(msgParts, fmt.Sprintf("Extracted %d fields", len(entry.Config.ExtractFields)))
	}
	if entry.Config.FlattenPath != "" {
		msgParts = append(msgParts, fmt.Sprintf("Flattened %s into rows", entry.Config.FlattenPath))
	}
	msg := strings.Join(msgParts, "; ")
	if msg == "" {
		msg = "Passed through (no filter rules)"
	}

	s.logger.Info("Filter applied", "connection_id", connectionID, "passed", passed, "dropped", dropped, "extracted", len(entry.Config.ExtractFields))
	s.emitEvent(connectionID, FilterEvent{
		Type:    "passed",
		Message: msg,
		Time:    now(),
		Rules:   len(entry.Config.Rules),
	})
}

// --- Field extraction ---

// flattenData unrolls a nested array into flat rows.
// It navigates to flatten_path to find the array, then for each element
// extracts flatten_fields and adds flatten_include fields from the parent.
func flattenData(data interface{}, cfg *FilterNodeConfig) interface{} {
	root, ok := data.(map[string]interface{})
	if !ok {
		return data
	}

	// Navigate to the array at flatten_path
	arr := navigateToArray(root, cfg.FlattenPath)
	if arr == nil {
		return data
	}

	var rows []interface{}
	for _, item := range arr {
		row := make(map[string]interface{})

		// Extract fields from each array element
		if elem, ok := item.(map[string]interface{}); ok {
			for srcPath, destName := range cfg.FlattenFields {
				if val, found := getNestedField(elem, srcPath); found {
					row[destName] = val
				}
			}
		}

		// Include parent-level fields in each row
		for srcPath, destName := range cfg.FlattenInclude {
			if val := getPathWithIndex(root, srcPath); val != nil {
				row[destName] = val
			}
		}

		rows = append(rows, row)
	}

	return rows
}

// navigateToArray walks a dot-separated path and returns the array at that location.
func navigateToArray(obj map[string]interface{}, path string) []interface{} {
	parts := strings.Split(path, ".")
	current := interface{}(obj)
	for _, part := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current, ok = m[part]
		if !ok {
			return nil
		}
	}
	arr, ok := current.([]interface{})
	if !ok {
		return nil
	}
	return arr
}

// getPathWithIndex resolves a path that may contain array index notation like "geometry.coordinates[0]".
func getPathWithIndex(obj map[string]interface{}, path string) interface{} {
	current := interface{}(obj)
	// Split by "." but also handle [N] indexing
	parts := strings.Split(path, ".")
	for _, part := range parts {
		// Check for array index: "field[0]"
		if idx := strings.Index(part, "["); idx >= 0 {
			field := part[:idx]
			indexStr := strings.TrimSuffix(part[idx+1:], "]")
			index, err := strconv.Atoi(indexStr)
			if err != nil {
				return nil
			}
			// Navigate to the field
			m, ok := current.(map[string]interface{})
			if !ok {
				return nil
			}
			arr, ok := m[field].([]interface{})
			if !ok || index < 0 || index >= len(arr) {
				return nil
			}
			current = arr[index]
		} else {
			m, ok := current.(map[string]interface{})
			if !ok {
				return nil
			}
			current, ok = m[part]
			if !ok {
				return nil
			}
		}
	}
	return current
}

// extractFields keeps only the specified JSON paths from the data.
// Paths use dot notation: "geometry.coordinates", "properties.timeseries.time"
// When a path crosses an array, it extracts from each element.
func extractFields(data interface{}, paths []string) interface{} {
	switch d := data.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{})
		for _, path := range paths {
			setExtractedField(result, d, strings.Split(path, "."))
		}
		return result
	case []interface{}:
		var result []interface{}
		for _, item := range d {
			if obj, ok := item.(map[string]interface{}); ok {
				extracted := make(map[string]interface{})
				for _, path := range paths {
					setExtractedField(extracted, obj, strings.Split(path, "."))
				}
				result = append(result, extracted)
			}
		}
		return result
	default:
		return data
	}
}

// setExtractedField navigates the source using parts and sets the value in dest,
// preserving the nested structure. Handles arrays transparently.
func setExtractedField(dest map[string]interface{}, source map[string]interface{}, parts []string) {
	if len(parts) == 0 {
		return
	}

	key := parts[0]
	val, ok := source[key]
	if !ok {
		return
	}

	// Last part — copy the value directly
	if len(parts) == 1 {
		dest[key] = val
		return
	}

	// More parts to go — recurse into the structure
	remaining := parts[1:]

	switch v := val.(type) {
	case map[string]interface{}:
		// Ensure dest has a map at this key
		sub, ok := dest[key].(map[string]interface{})
		if !ok {
			sub = make(map[string]interface{})
			dest[key] = sub
		}
		setExtractedField(sub, v, remaining)
	case []interface{}:
		// Extract from each element of the array
		destArr, _ := dest[key].([]interface{})
		// Ensure destArr is same length as source array
		for len(destArr) < len(v) {
			destArr = append(destArr, make(map[string]interface{}))
		}
		for i, item := range v {
			if obj, ok := item.(map[string]interface{}); ok {
				sub, ok := destArr[i].(map[string]interface{})
				if !ok {
					sub = make(map[string]interface{})
					destArr[i] = sub
				}
				setExtractedField(sub, obj, remaining)
			}
		}
		dest[key] = destArr
	default:
		// Path goes deeper but value is a leaf — nothing to extract
	}
}

// --- Rule evaluation ---

func evaluateRules(obj map[string]interface{}, rules []FilterRule, logic string) bool {
	if len(rules) == 0 {
		return true
	}

	for _, rule := range rules {
		result := evaluateRule(obj, rule)
		if logic == "or" && result {
			return true
		}
		if logic == "and" && !result {
			return false
		}
	}

	if logic == "or" {
		return false
	}
	return true // all passed for "and"
}

func evaluateRule(obj map[string]interface{}, rule FilterRule) bool {
	val, exists := getNestedField(obj, rule.Field)

	switch rule.Operator {
	case "is_empty":
		return !exists || val == nil || fmt.Sprintf("%v", val) == ""
	case "is_not_empty":
		return exists && val != nil && fmt.Sprintf("%v", val) != ""
	case "equals":
		return exists && fmt.Sprintf("%v", val) == rule.Value
	case "not_equals":
		return !exists || fmt.Sprintf("%v", val) != rule.Value
	case "contains":
		return exists && strings.Contains(fmt.Sprintf("%v", val), rule.Value)
	case "not_contains":
		return !exists || !strings.Contains(fmt.Sprintf("%v", val), rule.Value)
	case "gt":
		return compareNumeric(val, rule.Value) > 0
	case "gte":
		return compareNumeric(val, rule.Value) >= 0
	case "lt":
		return compareNumeric(val, rule.Value) < 0
	case "lte":
		return compareNumeric(val, rule.Value) <= 0
	case "starts_with":
		return exists && strings.HasPrefix(fmt.Sprintf("%v", val), rule.Value)
	case "ends_with":
		return exists && strings.HasSuffix(fmt.Sprintf("%v", val), rule.Value)
	default:
		return true
	}
}

func compareNumeric(val interface{}, target string) int {
	var a, b float64
	switch v := val.(type) {
	case float64:
		a = v
	case string:
		a, _ = strconv.ParseFloat(v, 64)
	default:
		return 0
	}
	b, _ = strconv.ParseFloat(target, 64)
	if a > b {
		return 1
	}
	if a < b {
		return -1
	}
	return 0
}

func getNestedField(obj map[string]interface{}, path string) (interface{}, bool) {
	parts := strings.Split(path, ".")
	current := interface{}(obj)
	for _, part := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// --- Pipeline info with predecessor-based routing ---

func (s *FilterService) getPipelineInfo(ctx context.Context, connectionID string) (*PipelineInfo, error) {
	s.pipelineCacheMu.RLock()
	if info, ok := s.pipelineCache[connectionID]; ok {
		if time.Since(s.pipelineCacheTime[connectionID]) < s.pipelineCacheTTL {
			s.pipelineCacheMu.RUnlock()
			return info, nil
		}
	}
	s.pipelineCacheMu.RUnlock()

	var nodesJSON, edgesJSON []byte
	err := s.db.QueryRowContext(ctx, `SELECT nodes, edges FROM connections WHERE id = $1`, connectionID).Scan(&nodesJSON, &edgesJSON)
	if err != nil {
		return nil, err
	}

	var nodes []struct {
		ID     string          `json:"id"`
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, err
	}

	var edges []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if edgesJSON != nil {
		_ = json.Unmarshal(edgesJSON, &edges)
	}

	nodeTypes := make(map[string]string)
	for _, n := range nodes {
		nodeTypes[n.ID] = n.Type
	}

	var entries []*FilterEntry
	for _, node := range nodes {
		if node.Type != "filter" {
			continue
		}
		var cfg FilterNodeConfig
		if err := json.Unmarshal(node.Config, &cfg); err != nil {
			continue
		}

		var predID string
		var predIsConsumer bool
		for _, e := range edges {
			if e.Target == node.ID {
				predID = e.Source
				predIsConsumer = nodeTypes[e.Source] == "consumer"
				break
			}
		}

		entries = append(entries, &FilterEntry{
			NodeID:         node.ID,
			Config:         &cfg,
			PredecessorID:  predID,
			PredIsConsumer: predIsConsumer,
		})
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("no filter node found")
	}

	info := &PipelineInfo{Entries: entries}
	s.pipelineCacheMu.Lock()
	s.pipelineCache[connectionID] = info
	s.pipelineCacheTime[connectionID] = time.Now()
	s.pipelineCacheMu.Unlock()

	return info, nil
}

// --- Events ---

func (s *FilterService) subscribeEvents(connectionID string) (chan FilterEvent, func()) {
	ch := make(chan FilterEvent, 50)
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

func (s *FilterService) emitEvent(connectionID string, event FilterEvent) {
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

func (s *FilterService) getRecentEvents(connectionID string) []FilterEvent {
	s.recentEventsMu.RLock()
	defer s.recentEventsMu.RUnlock()
	cp := make([]FilterEvent, len(s.recentEvents[connectionID]))
	copy(cp, s.recentEvents[connectionID])
	return cp
}

// --- HTTP server ---

func startHTTPServer(port string, service *FilterService, logger *slog.Logger) {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
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

		fmt.Fprintf(w, "data: {\"type\":\"connected\",\"message\":\"Listening for filter events\"}\n\n")
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
		logger.Info("Filter HTTP server started", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error", "error", err)
		}
	}()
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func initNATS(natsURL string, logger *slog.Logger) (*nats.Conn, error) {
	opts := []nats.Option{
		nats.Name("VRSky-Data-Filter"),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) { logger.Warn("NATS disconnected", "error", err) }),
		nats.ReconnectHandler(func(nc *nats.Conn) { logger.Info("NATS reconnected") }),
	}
	return nats.Connect(natsURL, opts...)
}
