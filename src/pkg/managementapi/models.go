package managementapi

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Connection represents a data pipeline connection
type Connection struct {
	ID                string            `json:"id" db:"id"`
	TenantID          string            `json:"tenant_id" db:"tenant_id"`
	Name              string            `json:"name" db:"name"`
	Description       string            `json:"description" db:"description"`
	SourceConfig      SourceConfig      `json:"source_config" db:"source_config"`
	ConverterConfig   ConverterConfig   `json:"converter_config" db:"converter_config"`
	FilterConfig      FilterConfig      `json:"filter_config" db:"filter_config"`
	DestinationConfig DestinationConfig `json:"destination_config" db:"destination_config"`
	Status            string            `json:"status" db:"status"` // stopped, running, error
	CreatedAt         time.Time         `json:"created_at" db:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at" db:"updated_at"`
	StartedAt         *time.Time        `json:"started_at" db:"started_at"`
	StoppedAt         *time.Time        `json:"stopped_at" db:"stopped_at"`
	LastError         *string           `json:"last_error" db:"last_error"`
}

// SourceConfig represents the source/consumer configuration
type SourceConfig struct {
	Type     string                `json:"type"` // http, file, database
	HTTP     *HTTPSourceConfig     `json:"http,omitempty"`
	File     *FileSourceConfig     `json:"file,omitempty"`
	Database *DatabaseSourceConfig `json:"database,omitempty"`
}

// HTTPSourceConfig represents HTTP source/webhook configuration
type HTTPSourceConfig struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"` // GET, POST, etc.
	Headers map[string]string `json:"headers,omitempty"`
	Auth    *AuthConfig       `json:"auth,omitempty"`
	Polling *PollingConfig    `json:"polling,omitempty"` // For polling mode
}

// FileSourceConfig represents file source configuration
type FileSourceConfig struct {
	Path     string `json:"path"`
	Pattern  string `json:"pattern,omitempty"`  // Regex pattern for file names
	Encoding string `json:"encoding,omitempty"` // utf-8, latin-1, etc.
	Watch    bool   `json:"watch"`              // Watch for file changes
}

// DatabaseSourceConfig represents database source configuration
type DatabaseSourceConfig struct {
	ConnectionString string `json:"connection_string"`
	Query            string `json:"query"`
	PollInterval     int    `json:"poll_interval,omitempty"` // Seconds, 0 = no polling
}

// ConverterConfig represents the converter configuration
type ConverterConfig struct {
	SchemaValidator *SchemaValidatorConfig `json:"schema_validator,omitempty"`
	FieldMapper     *FieldMapperConfig     `json:"field_mapper,omitempty"`
	RuleEngine      *RuleEngineConfig      `json:"rule_engine,omitempty"`
}

// SchemaValidatorConfig represents JSON schema validation configuration
type SchemaValidatorConfig struct {
	InputSchema  json.RawMessage `json:"input_schema"`  // JSON schema
	OutputSchema json.RawMessage `json:"output_schema"` // JSON schema
}

// FieldMapperConfig represents field mapping configuration
type FieldMapperConfig struct {
	Mappings map[string]string `json:"mappings"` // source_field -> dest_field
}

// RuleEngineConfig represents rule engine configuration
type RuleEngineConfig struct {
	Rules []Rule `json:"rules"`
}

// Rule represents a single transformation rule
type Rule struct {
	Name           string `json:"name"`
	Condition      string `json:"condition"`      // Expression that evaluates to bool
	Transformation string `json:"transformation"` // Expression that transforms the data
}

// FilterConfig represents the filter configuration
type FilterConfig struct {
	Rules []*FilterRule `json:"rules,omitempty"`
	WASM  *WASMConfig   `json:"wasm,omitempty"`
}

// FilterRule represents a filter rule
type FilterRule struct {
	Name      string `json:"name"`
	Condition string `json:"condition"` // Expression that evaluates to bool
}

// WASMConfig represents WASM filter configuration
type WASMConfig struct {
	Binary []byte `json:"binary"` // Base64 encoded WASM binary
}

// DestinationConfig represents the destination/producer configuration
type DestinationConfig struct {
	Type     string                     `json:"type"` // http, file, database
	HTTP     *HTTPDestinationConfig     `json:"http,omitempty"`
	File     *FileDestinationConfig     `json:"file,omitempty"`
	Database *DatabaseDestinationConfig `json:"database,omitempty"`
}

// HTTPDestinationConfig represents HTTP destination configuration
type HTTPDestinationConfig struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"` // POST, PUT, etc.
	Headers map[string]string `json:"headers,omitempty"`
	Auth    *AuthConfig       `json:"auth,omitempty"`
}

// FileDestinationConfig represents file destination configuration
type FileDestinationConfig struct {
	Path      string `json:"path"`
	Format    string `json:"format,omitempty"` // json, csv, etc.
	Append    bool   `json:"append"`
	CreateDir bool   `json:"create_dir"`
}

// DatabaseDestinationConfig represents database destination configuration
type DatabaseDestinationConfig struct {
	ConnectionString string `json:"connection_string"`
	Query            string `json:"query"` // INSERT/UPDATE query
	BatchSize        int    `json:"batch_size,omitempty"`
}

// AuthConfig represents authentication configuration
type AuthConfig struct {
	Type   string            `json:"type"` // basic, bearer, oauth, api_key
	Basic  *BasicAuthConfig  `json:"basic,omitempty"`
	Bearer *BearerAuthConfig `json:"bearer,omitempty"`
	APIKey *APIKeyAuthConfig `json:"api_key,omitempty"`
}

// BasicAuthConfig represents HTTP Basic authentication
type BasicAuthConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// BearerAuthConfig represents Bearer token authentication
type BearerAuthConfig struct {
	Token string `json:"token"`
}

// APIKeyAuthConfig represents API key authentication
type APIKeyAuthConfig struct {
	HeaderName string `json:"header_name"`
	Key        string `json:"key"`
}

// PollingConfig represents polling configuration
type PollingConfig struct {
	Interval int `json:"interval"` // Seconds
	Timeout  int `json:"timeout"`  // Seconds
}

// ConnectionEvent represents an event in the connection lifecycle
type ConnectionEvent struct {
	ID           string          `json:"id" db:"id"`
	ConnectionID string          `json:"connection_id" db:"connection_id"`
	TenantID     string          `json:"tenant_id" db:"tenant_id"`
	EventType    string          `json:"event_type" db:"event_type"` // started, stopped, error, metrics_snapshot, config_changed
	EventData    json.RawMessage `json:"event_data" db:"event_data"`
	CreatedAt    time.Time       `json:"created_at" db:"created_at"`
}

// Metrics represents real-time metrics for a connection
type Metrics struct {
	ConnectionID   string    `json:"connection_id"`
	TenantID       string    `json:"tenant_id"`
	Timestamp      time.Time `json:"timestamp"`
	Input          int64     `json:"input"`           // Messages received
	AfterFilter    int64     `json:"after_filter"`    // After filtering
	AfterConverter int64     `json:"after_converter"` // After conversion
	Output         int64     `json:"output"`          // Successfully sent
	Errors         int64     `json:"errors"`          // Failed messages
	Throughput     float64   `json:"throughput"`      // Messages/sec
}

// MetricsSnapshot is a snapshot of the latest metrics
type MetricsSnapshot struct {
	ConnectionID   string    `json:"connection_id"`
	TenantID       string    `json:"tenant_id"`
	Timestamp      time.Time `json:"timestamp"`
	Input          int64     `json:"input"`
	AfterFilter    int64     `json:"after_filter"`
	AfterConverter int64     `json:"after_converter"`
	Output         int64     `json:"output"`
	Errors         int64     `json:"errors"`
	Throughput     float64   `json:"throughput"`
	IsRunning      bool      `json:"is_running"`
	LastUpdateTime time.Time `json:"last_update_time"`
}

// Value implements sql.Valuer for JSONB types
func (s SourceConfig) Value() (driver.Value, error) {
	return json.Marshal(s)
}

// Scan implements sql.Scanner for JSONB types
func (s *SourceConfig) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("cannot convert %T to []byte", value)
	}
	return json.Unmarshal(bytes, s)
}

// Similar implementations for other JSONB types
func (c ConverterConfig) Value() (driver.Value, error) {
	return json.Marshal(c)
}

func (c *ConverterConfig) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("cannot convert %T to []byte", value)
	}
	return json.Unmarshal(bytes, c)
}

func (f FilterConfig) Value() (driver.Value, error) {
	return json.Marshal(f)
}

func (f *FilterConfig) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("cannot convert %T to []byte", value)
	}
	return json.Unmarshal(bytes, f)
}

func (d DestinationConfig) Value() (driver.Value, error) {
	return json.Marshal(d)
}

func (d *DestinationConfig) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("cannot convert %T to []byte", value)
	}
	return json.Unmarshal(bytes, d)
}

// CreateConnectionRequest is the request to create a new connection
type CreateConnectionRequest struct {
	Name              string            `json:"name"`
	Description       string            `json:"description"`
	SourceConfig      SourceConfig      `json:"source_config"`
	ConverterConfig   ConverterConfig   `json:"converter_config"`
	FilterConfig      FilterConfig      `json:"filter_config"`
	DestinationConfig DestinationConfig `json:"destination_config"`
}

// UpdateConnectionRequest is the request to update a connection
type UpdateConnectionRequest struct {
	Name              *string            `json:"name"`
	Description       *string            `json:"description"`
	SourceConfig      *SourceConfig      `json:"source_config"`
	ConverterConfig   *ConverterConfig   `json:"converter_config"`
	FilterConfig      *FilterConfig      `json:"filter_config"`
	DestinationConfig *DestinationConfig `json:"destination_config"`
}

// NewConnection creates a new Connection with default values
func NewConnection(tenantID string, req CreateConnectionRequest) *Connection {
	now := time.Now().UTC()
	return &Connection{
		ID:                uuid.New().String(),
		TenantID:          tenantID,
		Name:              req.Name,
		Description:       req.Description,
		SourceConfig:      req.SourceConfig,
		ConverterConfig:   req.ConverterConfig,
		FilterConfig:      req.FilterConfig,
		DestinationConfig: req.DestinationConfig,
		Status:            "stopped",
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

// NewConnectionEvent creates a new connection event
func NewConnectionEvent(connectionID, tenantID, eventType string, eventData json.RawMessage) *ConnectionEvent {
	return &ConnectionEvent{
		ID:           uuid.New().String(),
		ConnectionID: connectionID,
		TenantID:     tenantID,
		EventType:    eventType,
		EventData:    eventData,
		CreatedAt:    time.Now().UTC(),
	}
}
