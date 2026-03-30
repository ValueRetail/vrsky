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

// FieldMapping defines a single field transformation
type FieldMapping struct {
	Source     string      `json:"source"`               // source field path (e.g. "name")
	Target     string      `json:"target"`               // target field name (e.g. "full_name")
	Type       string      `json:"type,omitempty"`        // "rename", "copy", "static", "remove", "template"
	Value      interface{} `json:"value,omitempty"`       // for static type
	Expression string      `json:"expression,omitempty"`  // for template type
}

// ConverterNodeConfig is what the UI stores in the node config
type ConverterNodeConfig struct {
	Mappings     []FieldMapping `json:"mappings"`
	DropUnmapped bool           `json:"drop_unmapped"`

	// Format conversion
	OutputFormat string `json:"output_format"` // "", "csv", "tsv", "xml", "text", "yaml", "ndjson"
	CsvDelimiter string `json:"csv_delimiter"`
	CsvHeaders   *bool  `json:"csv_headers"`
	TextTemplate string `json:"text_template"`
	XmlRootTag   string `json:"xml_root_tag"`
	XmlRowTag    string `json:"xml_row_tag"`
}

type ConverterService struct {
	nc     *nats.Conn
	db     *sql.DB
	logger *slog.Logger
	config *Config

	configCache     map[string]*ConverterNodeConfig
	configCacheMu   sync.RWMutex
	configCacheTime map[string]time.Time
	configCacheTTL  time.Duration

	eventSubs      map[string][]chan ConvertEvent
	eventSubsMu    sync.RWMutex
	recentEvents   map[string][]ConvertEvent
	recentEventsMu sync.RWMutex

	stopCh    chan struct{}
	stoppedCh chan struct{}
}

type ConvertEvent struct {
	Type    string `json:"type"`              // "converted", "error", "info", "connected"
	Message string `json:"message,omitempty"`
	Time    string `json:"time"`
	Before  string `json:"before,omitempty"`  // original payload preview
	After   string `json:"after,omitempty"`   // transformed payload preview
	Fields  int    `json:"fields,omitempty"`  // number of fields mapped
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
		Port:              getEnv("CONVERTER_PORT", "9600"),
	}

	if config.DatabaseURL == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	logger.Info("Starting Data Converter Service", "version", "1.0.0")

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

	service := &ConverterService{
		nc:              nc,
		db:              db,
		logger:          logger,
		config:          config,
		configCache:     make(map[string]*ConverterNodeConfig),
		configCacheTime: make(map[string]time.Time),
		configCacheTTL:  5 * time.Minute,
		eventSubs:       make(map[string][]chan ConvertEvent),
		recentEvents:    make(map[string][]ConvertEvent),
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

	logger.Info("Data Converter Service running. Press Ctrl+C to stop.")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	logger.Info("Shutting down...")
	cancel()
	service.Stop()
}

func (s *ConverterService) Start(ctx context.Context) error {
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

func (s *ConverterService) Stop() {
	close(s.stopCh)
	<-s.stoppedCh
}

func (s *ConverterService) handleMessage(ctx context.Context, msg *nats.Msg) {
	var env envelope.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		s.logger.Error("Failed to unmarshal envelope", "error", err)
		return
	}

	// Skip messages already converted (prevent loop)
	if env.Metadata != nil {
		if _, ok := env.Metadata["_converted"]; ok {
			return
		}
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

	converterCfg, err := s.getConverterConfig(ctx, connectionID)
	if err != nil {
		s.logger.Debug("No converter config", "connection_id", connectionID)
		return
	}
	hasMapping := len(converterCfg.Mappings) > 0
	hasFormat := converterCfg.OutputFormat != ""
	if !hasMapping && !hasFormat {
		return // nothing to do
	}

	// Parse the payload as JSON
	var data interface{}
	payload := env.Payload
	if err := json.Unmarshal(payload, &data); err != nil {
		s.emitEvent(connectionID, ConvertEvent{Type: "error", Message: "Payload is not valid JSON: " + err.Error(), Time: now()})
		return
	}

	beforePreview := string(payload)
	if len(beforePreview) > 2000 {
		beforePreview = beforePreview[:2000] + "..."
	}

	// Step 1: Apply field mappings (if any)
	var transformed interface{} = data
	var fieldCount int
	if hasMapping {
		switch d := data.(type) {
		case []interface{}:
			result := make([]interface{}, 0, len(d))
			for _, item := range d {
				if obj, ok := item.(map[string]interface{}); ok {
					mapped, n := applyMappings(obj, converterCfg)
					result = append(result, mapped)
					fieldCount = n
				} else {
					result = append(result, item)
				}
			}
			transformed = result
		case map[string]interface{}:
			mapped, n := applyMappings(d, converterCfg)
			transformed = mapped
			fieldCount = n
		}
	}

	// Step 2: Apply format conversion (if any)
	var newPayload []byte
	var newContentType string
	formatLabel := "JSON"

	if hasFormat {
		rows := toRows(transformed)
		var formatted string
		formatted, newContentType, formatLabel = convertFormat(rows, converterCfg)
		newPayload = []byte(formatted)
	} else {
		var err error
		newPayload, err = json.Marshal(transformed)
		if err != nil {
			s.emitEvent(connectionID, ConvertEvent{Type: "error", Message: "Failed to marshal: " + err.Error(), Time: now()})
			return
		}
		newContentType = "application/json"
	}

	afterPreview := string(newPayload)
	if len(afterPreview) > 2000 {
		afterPreview = afterPreview[:2000] + "..."
	}

	// Update envelope
	env.Payload = newPayload
	if newContentType != "" {
		env.ContentType = newContentType
	}
	if env.Metadata == nil {
		env.Metadata = make(map[string]interface{})
	}
	env.Metadata["_converted"] = true
	if hasFormat {
		env.Metadata["_output_format"] = converterCfg.OutputFormat
	}

	envData, err := json.Marshal(env)
	if err != nil {
		return
	}

	if err := s.nc.Publish(msg.Subject, envData); err != nil {
		s.emitEvent(connectionID, ConvertEvent{Type: "error", Message: "Failed to re-publish: " + err.Error(), Time: now()})
		return
	}

	msg2 := fmt.Sprintf("Converted to %s", formatLabel)
	if fieldCount > 0 {
		msg2 = fmt.Sprintf("Mapped %d fields, converted to %s", fieldCount, formatLabel)
	}
	s.logger.Info("Data converted", "connection_id", connectionID, "format", formatLabel, "fields", fieldCount)
	s.emitEvent(connectionID, ConvertEvent{
		Type:    "converted",
		Message: msg2,
		Time:    now(),
		Before:  beforePreview,
		After:   afterPreview,
		Fields:  fieldCount,
	})
}

func applyMappings(obj map[string]interface{}, cfg *ConverterNodeConfig) (map[string]interface{}, int) {
	result := make(map[string]interface{})
	applied := 0

	// If not dropping unmapped, start with a copy
	if !cfg.DropUnmapped {
		for k, v := range obj {
			result[k] = v
		}
	}

	for _, m := range cfg.Mappings {
		switch m.Type {
		case "rename", "":
			// Rename: take source field, put as target field
			if val, ok := getNestedField(obj, m.Source); ok {
				if !cfg.DropUnmapped {
					delete(result, m.Source)
				}
				setNestedField(result, m.Target, val)
				applied++
			}
		case "copy":
			// Copy: copy source to target (keep source)
			if val, ok := getNestedField(obj, m.Source); ok {
				setNestedField(result, m.Target, val)
				applied++
			}
		case "static":
			// Static: set target to a static value
			setNestedField(result, m.Target, m.Value)
			applied++
		case "remove":
			// Remove: delete the source field
			delete(result, m.Source)
			applied++
		case "template":
			// Template: simple string interpolation using {field} syntax
			if m.Expression != "" {
				value := m.Expression
				for k, v := range obj {
					value = strings.ReplaceAll(value, "{"+k+"}", fmt.Sprintf("%v", v))
				}
				setNestedField(result, m.Target, value)
				applied++
			}
		case "to_string":
			if val, ok := getNestedField(obj, m.Source); ok {
				setNestedField(result, m.Target, fmt.Sprintf("%v", val))
				applied++
			}
		case "to_number":
			if val, ok := getNestedField(obj, m.Source); ok {
				if f, err := toFloat(val); err == nil {
					setNestedField(result, m.Target, f)
					applied++
				}
			}
		}
	}

	return result, applied
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

func setNestedField(obj map[string]interface{}, path string, value interface{}) {
	parts := strings.Split(path, ".")
	current := obj
	for i, part := range parts {
		if i == len(parts)-1 {
			current[part] = value
		} else {
			next, ok := current[part].(map[string]interface{})
			if !ok {
				next = make(map[string]interface{})
				current[part] = next
			}
			current = next
		}
	}
}

func toFloat(val interface{}) (float64, error) {
	switch v := val.(type) {
	case float64:
		return v, nil
	case string:
		return strconv.ParseFloat(v, 64)
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	default:
		return 0, fmt.Errorf("cannot convert to number")
	}
}

// --- Format conversion ---

// toRows normalizes data to []map[string]interface{}
func toRows(data interface{}) []map[string]interface{} {
	switch d := data.(type) {
	case []interface{}:
		rows := make([]map[string]interface{}, 0, len(d))
		for _, item := range d {
			if obj, ok := item.(map[string]interface{}); ok {
				rows = append(rows, obj)
			}
		}
		return rows
	case map[string]interface{}:
		return []map[string]interface{}{d}
	default:
		return nil
	}
}

// stableKeys returns sorted keys from first row for consistent column order
func stableKeys(rows []map[string]interface{}) []string {
	if len(rows) == 0 {
		return nil
	}
	keys := make([]string, 0, len(rows[0]))
	for k := range rows[0] {
		keys = append(keys, k)
	}
	// Sort for deterministic output
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] > keys[j] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

func convertFormat(rows []map[string]interface{}, cfg *ConverterNodeConfig) (string, string, string) {
	switch cfg.OutputFormat {
	case "csv", "tsv":
		return convertCSV(rows, cfg), "text/csv", strings.ToUpper(cfg.OutputFormat)
	case "xml":
		return convertXML(rows, cfg), "application/xml", "XML"
	case "text":
		return convertText(rows, cfg), "text/plain", "Plain Text"
	case "yaml":
		return convertYAML(rows), "text/yaml", "YAML"
	case "ndjson":
		return convertNDJSON(rows), "application/x-ndjson", "NDJSON"
	default:
		data, _ := json.MarshalIndent(rows, "", "  ")
		return string(data), "application/json", "JSON"
	}
}

func convertCSV(rows []map[string]interface{}, cfg *ConverterNodeConfig) string {
	if len(rows) == 0 {
		return ""
	}
	delim := cfg.CsvDelimiter
	if delim == "" {
		delim = ","
	}
	if cfg.OutputFormat == "tsv" {
		delim = "\t"
	}

	keys := stableKeys(rows)
	var sb strings.Builder

	// Headers
	includeHeaders := cfg.CsvHeaders == nil || *cfg.CsvHeaders
	if includeHeaders {
		sb.WriteString(strings.Join(keys, delim))
		sb.WriteString("\n")
	}

	for _, row := range rows {
		vals := make([]string, len(keys))
		for i, k := range keys {
			v := row[k]
			s := fmt.Sprintf("%v", v)
			if v == nil {
				s = ""
			}
			// Quote if contains delimiter or newline
			if strings.ContainsAny(s, delim+"\n\"") {
				s = "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
			}
			vals[i] = s
		}
		sb.WriteString(strings.Join(vals, delim))
		sb.WriteString("\n")
	}
	return sb.String()
}

func convertXML(rows []map[string]interface{}, cfg *ConverterNodeConfig) string {
	rootTag := cfg.XmlRootTag
	if rootTag == "" {
		rootTag = "records"
	}
	rowTag := cfg.XmlRowTag
	if rowTag == "" {
		rowTag = "record"
	}

	var sb strings.Builder
	sb.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	sb.WriteString("<" + rootTag + ">\n")
	for _, row := range rows {
		sb.WriteString("  <" + rowTag + ">\n")
		for k, v := range row {
			val := fmt.Sprintf("%v", v)
			if v == nil {
				val = ""
			}
			// Escape XML special chars
			val = strings.ReplaceAll(val, "&", "&amp;")
			val = strings.ReplaceAll(val, "<", "&lt;")
			val = strings.ReplaceAll(val, ">", "&gt;")
			sb.WriteString("    <" + k + ">" + val + "</" + k + ">\n")
		}
		sb.WriteString("  </" + rowTag + ">\n")
	}
	sb.WriteString("</" + rootTag + ">\n")
	return sb.String()
}

func convertText(rows []map[string]interface{}, cfg *ConverterNodeConfig) string {
	tmpl := cfg.TextTemplate
	if tmpl == "" {
		// Default: tab-separated values
		keys := stableKeys(rows)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = "{" + k + "}"
		}
		tmpl = strings.Join(parts, "\t")
	}

	var sb strings.Builder
	for _, row := range rows {
		line := tmpl
		for k, v := range row {
			val := fmt.Sprintf("%v", v)
			if v == nil {
				val = ""
			}
			line = strings.ReplaceAll(line, "{"+k+"}", val)
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}

func convertYAML(rows []map[string]interface{}) string {
	var sb strings.Builder
	for i, row := range rows {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("- ")
		first := true
		for k, v := range row {
			if !first {
				sb.WriteString("  ")
			}
			val := fmt.Sprintf("%v", v)
			if v == nil {
				val = "null"
			} else if _, ok := v.(string); ok {
				// Quote strings that could be ambiguous
				val = "\"" + strings.ReplaceAll(val, "\"", "\\\"") + "\""
			}
			sb.WriteString(k + ": " + val + "\n")
			first = false
		}
	}
	return sb.String()
}

func convertNDJSON(rows []map[string]interface{}) string {
	var sb strings.Builder
	for _, row := range rows {
		data, _ := json.Marshal(row)
		sb.Write(data)
		sb.WriteString("\n")
	}
	return sb.String()
}

// --- Config lookup ---

func (s *ConverterService) getConverterConfig(ctx context.Context, connectionID string) (*ConverterNodeConfig, error) {
	s.configCacheMu.RLock()
	if cfg, ok := s.configCache[connectionID]; ok {
		if time.Since(s.configCacheTime[connectionID]) < s.configCacheTTL {
			s.configCacheMu.RUnlock()
			return cfg, nil
		}
	}
	s.configCacheMu.RUnlock()

	var nodesJSON []byte
	err := s.db.QueryRowContext(ctx, `SELECT nodes FROM connections WHERE id = $1`, connectionID).Scan(&nodesJSON)
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

	for _, node := range nodes {
		if node.Type != "converter" {
			continue
		}
		var cfg ConverterNodeConfig
		if err := json.Unmarshal(node.Config, &cfg); err != nil {
			continue
		}

		s.configCacheMu.Lock()
		s.configCache[connectionID] = &cfg
		s.configCacheTime[connectionID] = time.Now()
		s.configCacheMu.Unlock()

		return &cfg, nil
	}

	return nil, fmt.Errorf("no converter config found")
}

// --- Events ---

func (s *ConverterService) subscribeEvents(connectionID string) (chan ConvertEvent, func()) {
	ch := make(chan ConvertEvent, 50)
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

func (s *ConverterService) emitEvent(connectionID string, event ConvertEvent) {
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

func (s *ConverterService) getRecentEvents(connectionID string) []ConvertEvent {
	s.recentEventsMu.RLock()
	defer s.recentEventsMu.RUnlock()
	cp := make([]ConvertEvent, len(s.recentEvents[connectionID]))
	copy(cp, s.recentEvents[connectionID])
	return cp
}

// --- HTTP server ---

func startHTTPServer(port string, service *ConverterService, logger *slog.Logger) {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Preview endpoint: test transformations without deploying
	mux.HandleFunc("/preview/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		var req struct {
			Mappings     []FieldMapping `json:"mappings"`
			DropUnmapped bool           `json:"drop_unmapped"`
			SampleData   interface{}    `json:"sample_data"` // can be JSON object/array or string containing JSON
			OutputFormat string         `json:"output_format"`
			CsvDelimiter string         `json:"csv_delimiter"`
			CsvHeaders   *bool          `json:"csv_headers"`
			TextTemplate string         `json:"text_template"`
			XmlRootTag   string         `json:"xml_root_tag"`
			XmlRowTag    string         `json:"xml_row_tag"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		// Parse sample_data — it might be a string containing JSON
		var data interface{} = req.SampleData
		if s, ok := req.SampleData.(string); ok {
			if err := json.Unmarshal([]byte(s), &data); err != nil {
				w.Header().Set("Content-Type", "application/json")
				resp, _ := json.Marshal(map[string]interface{}{"ok": false, "error": "Sample data is not valid JSON: " + err.Error()})
				_, _ = w.Write(resp)
				return
			}
		}

		cfg := &ConverterNodeConfig{
			Mappings: req.Mappings, DropUnmapped: req.DropUnmapped,
			OutputFormat: req.OutputFormat, CsvDelimiter: req.CsvDelimiter,
			CsvHeaders: req.CsvHeaders, TextTemplate: req.TextTemplate,
			XmlRootTag: req.XmlRootTag, XmlRowTag: req.XmlRowTag,
		}

		// Step 1: Apply field mappings
		var transformed interface{} = data
		if len(cfg.Mappings) > 0 {
			switch d := data.(type) {
			case []interface{}:
				arr := make([]interface{}, 0, len(d))
				for _, item := range d {
					if obj, ok := item.(map[string]interface{}); ok {
						mapped, _ := applyMappings(obj, cfg)
						arr = append(arr, mapped)
					} else {
						arr = append(arr, item)
					}
				}
				transformed = arr
			case map[string]interface{}:
				mapped, _ := applyMappings(d, cfg)
				transformed = mapped
			}
		}

		// Step 2: Apply format conversion
		w.Header().Set("Content-Type", "application/json")
		if cfg.OutputFormat != "" {
			rows := toRows(transformed)
			formatted, _, _ := convertFormat(rows, cfg)
			resp, _ := json.Marshal(map[string]interface{}{"ok": true, "result_text": formatted})
			_, _ = w.Write(resp)
		} else {
			resp, _ := json.MarshalIndent(map[string]interface{}{"ok": true, "result": transformed}, "", "  ")
			_, _ = w.Write(resp)
		}
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

		fmt.Fprintf(w, "data: {\"type\":\"connected\",\"message\":\"Listening for converter events\"}\n\n")
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
		logger.Info("Converter HTTP server started", "port", port)
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
		nats.Name("VRSky-Data-Converter"),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) { logger.Warn("NATS disconnected", "error", err) }),
		nats.ReconnectHandler(func(nc *nats.Conn) { logger.Info("NATS reconnected") }),
	}
	return nats.Connect(natsURL, opts...)
}
