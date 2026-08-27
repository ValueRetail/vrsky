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

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/claimcheck"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
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
	Type       string      `json:"type,omitempty"`       // "rename", "copy", "static", "remove", "template"
	Value      interface{} `json:"value,omitempty"`      // for static type
	Expression string      `json:"expression,omitempty"` // for template type
}

// ConverterNodeConfig is what the UI stores in the node config
type ConverterNodeConfig struct {
	Mappings     []FieldMapping `json:"mappings"`
	DropUnmapped bool           `json:"drop_unmapped"`

	// Input parsing (ADR 0003). Empty InputFormat = auto-detect from the
	// envelope's ContentType, then a content sniff, then json.
	InputFormat        string `json:"input_format"`          // "", "json", "ndjson", "csv", "tsv", "xml", "yaml"
	InputCsvDelimiter  string `json:"input_csv_delimiter"`   // "" = sniff
	InputCsvNoHeader   bool   `json:"input_csv_no_header"`   // treat the first row as data
	InputCsvTrimSpace  bool   `json:"input_csv_trim_space"`  // trim whitespace in values
	InputXmlRecordPath string `json:"input_xml_record_path"` // REQUIRED for xml, e.g. "Orders.Order"
	InputXmlAttrPrefix string `json:"input_xml_attr_prefix"` // default "@"
	InputXmlTextKey    string `json:"input_xml_text_key"`    // default "#text"

	// Format conversion
	OutputFormat string `json:"output_format"` // "", "csv", "tsv", "xml", "text", "yaml", "ndjson"
	CsvDelimiter string `json:"csv_delimiter"`
	CsvHeaders   *bool  `json:"csv_headers"`
	TextTemplate string `json:"text_template"`
	XmlRootTag   string `json:"xml_root_tag"`
	XmlRowTag    string `json:"xml_row_tag"`
}

// ConverterEntry represents one converter node in the pipeline with its routing info
type ConverterEntry struct {
	NodeID         string
	Config         *ConverterNodeConfig
	PredecessorID  string // node that must have processed before us
	PredIsConsumer bool   // if true, process when _last_processed_by is empty
}

// ConverterPipelineInfo holds all converter entries for a connection
type ConverterPipelineInfo struct {
	Entries []*ConverterEntry
}

type ConverterService struct {
	nc     *nats.Conn
	pub    *messaging.Publisher // JetStream data-flow publisher (#70)
	db     *sql.DB
	logger *slog.Logger
	config *Config

	pipelineCache     map[string]*ConverterPipelineInfo
	pipelineCacheMu   sync.RWMutex
	pipelineCacheTime map[string]time.Time
	pipelineCacheTTL  time.Duration

	eventSubs      map[string][]chan ConvertEvent
	eventSubsMu    sync.RWMutex
	recentEvents   map[string][]ConvertEvent
	recentEventsMu sync.RWMutex

	// Claim-check (ADR 0002): this service is NOT on the SDK runner, so it must
	// rehydrate offloaded payloads on entry and offload large outputs itself.
	// spill is nil when PAYLOAD_STORE_* is unconfigured.
	spill        objectstore.ObjectStore
	inlineMax    int
	rehydrateMax int64

	stopCh    chan struct{}
	stoppedCh chan struct{}
}

type ConvertEvent struct {
	Type    string `json:"type"` // "converted", "error", "info", "connected"
	Message string `json:"message,omitempty"`
	Time    string `json:"time"`
	Before  string `json:"before,omitempty"` // original payload preview
	After   string `json:"after,omitempty"`  // transformed payload preview
	Fields  int    `json:"fields,omitempty"` // number of fields mapped
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

	jsCtx, jsErr := nc.JetStream()
	if jsErr != nil {
		logger.Error("Failed to get JetStream context", "error", jsErr)
		os.Exit(1)
	}
	service := &ConverterService{
		nc:                nc,
		pub:               messaging.NewPublisher(jsCtx),
		db:                db,
		logger:            logger,
		config:            config,
		pipelineCache:     make(map[string]*ConverterPipelineInfo),
		pipelineCacheTime: make(map[string]time.Time),
		pipelineCacheTTL:  5 * time.Minute,
		eventSubs:         make(map[string][]chan ConvertEvent),
		recentEvents:      make(map[string][]ConvertEvent),
		stopCh:            make(chan struct{}),
		stoppedCh:         make(chan struct{}),
		inlineMax:         claimcheck.InlineMaxFromEnv(logger),
		rehydrateMax:      claimcheck.RehydrateMaxFromEnv(logger),
	}
	spill, err := claimcheck.OpenStoreFromEnv(context.Background(), logger)
	if err != nil {
		logger.Error("Failed to open payload offload store", "error", err)
		os.Exit(1)
	}
	service.spill = spill
	if spill == nil {
		logger.Warn("no payload offload store configured (PAYLOAD_STORE_BUCKET unset); offloaded envelopes will be NAKed and large outputs cannot be offloaded")
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
	js, jsErr := s.nc.JetStream()
	if jsErr != nil {
		return fmt.Errorf("JetStream context: %w", jsErr)
	}
	sub, err := messaging.Subscribe(js, messaging.SubscriberOpts{
		DurableName: "data-converter",
		Logger:      s.logger,
	}, func(ctx context.Context, msg *nats.Msg) error {
		// Transform-logic failures (bad JSON, bad mapping config) are
		// deterministic and ack — errors surface via emitEvent. Infrastructure
		// failures — rehydrate, offload, publish — return an error so the
		// message is NAKed and retried rather than silently lost (ADR 0002).
		return s.handleMessage(ctx, msg)
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe via JetStream: %w", err)
	}
	s.logger.Info("Subscribed via JetStream", "durable", "data-converter")

	go func() {
		<-s.stopCh
		sub.Stop()
		close(s.stoppedCh)
	}()
	return nil
}

func (s *ConverterService) Stop() {
	close(s.stopCh)
	<-s.stoppedCh
}

func (s *ConverterService) handleMessage(ctx context.Context, msg *nats.Msg) error {
	var env envelope.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		s.logger.Error("Failed to unmarshal envelope", "error", err)
		return nil // will never parse — ack, don't burn retries
	}

	// Rehydrate an offloaded payload (claim-check) before converting — EXCEPT
	// when it is over the rehydrate cap and a store is available: then the ref
	// stays set and each entry takes the record-streaming path instead of
	// buffering (ADR 0002 phase B). Other rehydrate errors are infrastructure,
	// not converter logic: NAK so the message is retried (transient store blip)
	// or lands in the DLQ instead of being silently dropped as invalid JSON.
	overCap := env.PayloadRef != "" && s.rehydrateMax > 0 && env.PayloadSize > s.rehydrateMax && s.spill != nil
	if !overCap {
		if err := claimcheck.Rehydrate(ctx, s.spill, &env, s.rehydrateMax); err != nil {
			s.emitEvent(env.IntegrationID, ConvertEvent{Type: "error", Message: "Payload rehydrate failed: " + err.Error(), Time: now()})
			return fmt.Errorf("rehydrate: %w", err)
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
		return nil
	}

	info, err := s.getPipelineInfo(ctx, connectionID)
	if err != nil || info == nil || len(info.Entries) == 0 {
		return nil
	}

	// Find which converter entry should handle this message based on predecessor
	lastProcessedBy := ""
	if env.Metadata != nil {
		if v, ok := env.Metadata["_last_processed_by"].(string); ok {
			lastProcessedBy = v
		}
	}

	// Process ALL matching entries (not just the first) for branching pipelines
	for _, entry := range info.Entries {
		if entry.PredIsConsumer && lastProcessedBy != "" {
			continue
		}
		if !entry.PredIsConsumer && entry.PredecessorID != "" && lastProcessedBy != entry.PredecessorID {
			continue
		}

		// An infrastructure failure in any entry NAKs the whole message.
		// Redelivery reprocesses every entry (at-least-once; downstream sees
		// fresh envelope IDs), matching the SDK's semantics for connectors.
		if perr := s.processEntry(ctx, connectionID, msg.Subject, &env, entry); perr != nil && err == nil {
			err = perr
		}
	}
	return err
}

// processEntry applies one converter node and republishes the result. It
// returns an error only for infrastructure failures (offload, publish) — the
// caller NAKs those; transform-logic failures emit a UI event and ack, exactly
// as before.
func (s *ConverterService) processEntry(ctx context.Context, connectionID, subject string, origEnv *envelope.Envelope, entry *ConverterEntry) error {
	converterCfg := entry.Config
	hasMapping := len(converterCfg.Mappings) > 0
	hasFormat := converterCfg.OutputFormat != ""
	// A non-JSON input is a conversion in itself (ADR 0003): "CSV in, nothing
	// else configured" means "give me JSON", so it is not a no-op.
	if !hasMapping && !hasFormat && !converterCfg.transcodes(origEnv) {
		return nil
	}

	// An envelope still carrying a ref here is over the rehydrate cap — take the
	// record-streaming path (ADR 0002 phase B) instead of buffering it.
	if origEnv.PayloadRef != "" {
		return s.streamEntry(ctx, connectionID, origEnv, entry)
	}

	// Work on a copy of the original payload to avoid mutating shared state
	payload := make([]byte, len(origEnv.Payload))
	copy(payload, origEnv.Payload)

	// Parse the payload in whatever format this node accepts (ADR 0003): json
	// (default, unchanged code path), ndjson, csv/tsv, xml or yaml.
	data, perr := parsePayload(converterCfg, origEnv)
	if perr != nil {
		s.emitEvent(connectionID, ConvertEvent{Type: "error", Message: "Payload parse failed: " + perr.Error(), Time: now()})
		return nil
	}

	beforePreview := string(payload)
	if len(beforePreview) > 2000 {
		beforePreview = beforePreview[:2000] + "..."
	}

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
			return nil
		}
		newContentType = "application/json"
	}

	afterPreview := string(newPayload)
	if len(afterPreview) > 2000 {
		afterPreview = afterPreview[:2000] + "..."
	}

	// Build a new envelope copy for this branch. Fresh ID so the JetStream
	// MsgID dedup window doesn't drop this as a duplicate of the upstream
	// message (5-minute dedup window).
	env := *origEnv
	env.ID = uuid.New().String()
	env.Payload = newPayload
	if newContentType != "" {
		env.ContentType = newContentType
	}
	env.Metadata = make(map[string]interface{})
	for k, v := range origEnv.Metadata {
		env.Metadata[k] = v
	}
	env.Metadata["_last_processed_by"] = entry.NodeID
	env.Metadata["_converted"] = true
	env.Metadata["_source_envelope_id"] = origEnv.ID
	if hasFormat {
		env.Metadata["_output_format"] = converterCfg.OutputFormat
	}

	// Offload an over-threshold result (claim-check) so the published message
	// stays under NATS max_payload — conversion can INFLATE a payload (XML,
	// row-expanded formats), so this applies even when the input arrived inline.
	if _, err := claimcheck.OffloadIfLarge(ctx, s.spill, &env, s.inlineMax, s.logger); err != nil {
		s.emitEvent(connectionID, ConvertEvent{Type: "error", Message: "Payload offload failed: " + err.Error(), Time: now()})
		return fmt.Errorf("offload: %w", err)
	}

	envData, err := json.Marshal(env)
	if err != nil {
		return nil
	}

	if err := s.pub.Publish(ctx, env.TenantID, connectionID, env.ID, envData); err != nil {
		// Publish failures were previously logged and ACKED — output silently
		// lost. NAK instead so redelivery retries the publish.
		s.emitEvent(connectionID, ConvertEvent{Type: "error", Message: "Failed to re-publish: " + err.Error(), Time: now()})
		return fmt.Errorf("publish: %w", err)
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
	return nil
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
	delim := csvDelim(cfg)
	keys := stableKeys(rows)
	var sb strings.Builder

	// Headers
	includeHeaders := cfg.CsvHeaders == nil || *cfg.CsvHeaders
	if includeHeaders {
		sb.WriteString(strings.Join(keys, delim))
		sb.WriteString("\n")
	}

	for _, row := range rows {
		sb.WriteString(csvLine(keys, row, delim))
	}
	return sb.String()
}

// csvDelim resolves the effective delimiter for a csv/tsv node.
func csvDelim(cfg *ConverterNodeConfig) string {
	delim := cfg.CsvDelimiter
	if delim == "" {
		delim = ","
	}
	if cfg.OutputFormat == "tsv" {
		delim = "\t"
	}
	return delim
}

// csvLine renders one row against a pinned key order — shared by the buffered
// and streaming paths so their output cannot drift.
func csvLine(keys []string, row map[string]interface{}, delim string) string {
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
	return strings.Join(vals, delim) + "\n"
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
			// Sanitize the element name: a key with a space, '<', or leading
			// digit would otherwise emit invalid XML that downstream parsers
			// reject. Previously only the value was escaped.
			tag := xmlTagName(k)
			sb.WriteString("    <" + tag + ">" + val + "</" + tag + ">\n")
		}
		sb.WriteString("  </" + rowTag + ">\n")
	}
	sb.WriteString("</" + rootTag + ">\n")
	return sb.String()
}

// xmlTagName turns an arbitrary map key into a valid XML element name: it must
// start with a letter or underscore and otherwise contain only letters,
// digits, hyphens, underscores, or periods. Invalid characters become '_', and
// a leading non-letter/underscore (or empty key) is prefixed with '_'.
func xmlTagName(k string) string {
	var b strings.Builder
	for i, r := range k {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			b.WriteRune(r)
		case i > 0 && (r == '-' || r == '.' || (r >= '0' && r <= '9')):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := b.String()
	if s == "" {
		return "_"
	}
	if c := s[0]; !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
		return "_" + s
	}
	return s
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

// --- Pipeline info with predecessor-based routing ---

func (s *ConverterService) getPipelineInfo(ctx context.Context, connectionID string) (*ConverterPipelineInfo, error) {
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

	// Build node type lookup
	nodeTypes := make(map[string]string)
	for _, n := range nodes {
		nodeTypes[n.ID] = n.Type
	}

	// For each converter node, find its predecessor (incoming edge source)
	var entries []*ConverterEntry
	for _, node := range nodes {
		if node.Type != "converter" {
			continue
		}
		var cfg ConverterNodeConfig
		if err := json.Unmarshal(node.Config, &cfg); err != nil {
			continue
		}

		// Find incoming edge to this converter
		var predID string
		var predIsConsumer bool
		for _, e := range edges {
			if e.Target == node.ID {
				predID = e.Source
				predIsConsumer = nodeTypes[e.Source] == "consumer"
				break
			}
		}

		entries = append(entries, &ConverterEntry{
			NodeID:         node.ID,
			Config:         &cfg,
			PredecessorID:  predID,
			PredIsConsumer: predIsConsumer,
		})
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("no converter config found")
	}

	info := &ConverterPipelineInfo{Entries: entries}
	s.pipelineCacheMu.Lock()
	s.pipelineCache[connectionID] = info
	s.pipelineCacheTime[connectionID] = time.Now()
	s.pipelineCacheMu.Unlock()

	return info, nil
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

	// Liveness: the process is up. /healthz is the canonical Kubernetes path;
	// /health remains as a backward-compatible alias.
	liveness := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
	// Readiness: NATS must be connected (the only upstream this worker needs).
	readiness := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if service.nc == nil || !service.nc.IsConnected() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not ready","checks":{"nats":"error: not connected"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ready","checks":{"nats":"ok"}}`))
	}
	mux.HandleFunc("/health", liveness)
	mux.HandleFunc("/healthz", liveness)
	mux.HandleFunc("/ready", readiness)
	mux.HandleFunc("/readyz", readiness)

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
