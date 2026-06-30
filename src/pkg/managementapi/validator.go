package managementapi

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Validator provides configuration validation
type Validator struct {
	schemaCompiler *jsonschema.Compiler
}

// NewValidator creates a new validator
func NewValidator() *Validator {
	return &Validator{
		schemaCompiler: jsonschema.NewCompiler(),
	}
}

// ValidateConnection validates a complete connection configuration
func (v *Validator) ValidateConnection(conn *Connection) error {
	if conn == nil {
		return &BadRequestError{Message: "connection cannot be nil"}
	}

	if strings.TrimSpace(conn.Name) == "" {
		return &BadRequestError{Message: "connection name is required"}
	}

	if err := v.ValidateSourceConfig(&conn.SourceConfig); err != nil {
		return err
	}

	if err := v.ValidateConverterConfig(&conn.ConverterConfig); err != nil {
		return err
	}

	if err := v.ValidateFilterConfig(&conn.FilterConfig); err != nil {
		return err
	}

	if err := v.ValidateDestinationConfig(&conn.DestinationConfig); err != nil {
		return err
	}

	return nil
}

// ValidateSourceConfig validates source configuration
func (v *Validator) ValidateSourceConfig(config *SourceConfig) error {
	if config == nil {
		return &ConfigError{Component: "source", Field: "type", Reason: "source config is required"}
	}

	if strings.TrimSpace(config.Type) == "" {
		return &ConfigError{Component: "source", Field: "type", Reason: "type is required (http, file, or database)"}
	}

	switch config.Type {
	case "http":
		return v.validateHTTPSource(config.HTTP)
	case "file":
		return v.validateFileSource(config.File)
	case "database":
		return v.validateDatabaseSource(config.Database)
	case "tenant":
		if config.Tenant == nil || config.Tenant.ConnectionID == "" {
			return &ConfigError{Component: "source", Field: "tenant.connection_id", Reason: "connection_id is required for tenant source"}
		}
		return nil
	default:
		return &ConfigError{Component: "source", Field: "type", Reason: fmt.Sprintf("invalid type '%s', must be http, file, database, or tenant", config.Type)}
	}
}

// validateHTTPSource validates HTTP source configuration
func (v *Validator) validateHTTPSource(config *HTTPSourceConfig) error {
	if config == nil {
		return &ConfigError{Component: "source", Field: "http", Reason: "HTTP source config is required when type is 'http'"}
	}

	if strings.TrimSpace(config.URL) == "" {
		return &ConfigError{Component: "source", Field: "http.url", Reason: "URL is required"}
	}

	// Validate URL format
	parsedURL, err := url.Parse(config.URL)
	if err != nil {
		return &ConfigError{Component: "source", Field: "http.url", Reason: fmt.Sprintf("invalid URL format: %v", err)}
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return &ConfigError{Component: "source", Field: "http.url", Reason: fmt.Sprintf("URL must use http or https scheme, got '%s'", parsedURL.Scheme)}
	}

	// Validate method if provided
	if config.Method != "" && !isValidHTTPMethod(config.Method) {
		return &ConfigError{Component: "source", Field: "http.method", Reason: fmt.Sprintf("invalid HTTP method '%s'", config.Method)}
	}

	// Validate auth if provided
	if config.Auth != nil {
		if err := v.validateAuthConfig(config.Auth, "source.http.auth"); err != nil {
			return err
		}
	}

	return nil
}

// validateFileSource validates file source configuration
func (v *Validator) validateFileSource(config *FileSourceConfig) error {
	if config == nil {
		return &ConfigError{Component: "source", Field: "file", Reason: "file source config is required when type is 'file'"}
	}

	if strings.TrimSpace(config.Path) == "" {
		return &ConfigError{Component: "source", Field: "file.path", Reason: "path is required"}
	}

	// Validate regex pattern if provided
	if config.Pattern != "" {
		if _, err := regexp.Compile(config.Pattern); err != nil {
			return &ConfigError{Component: "source", Field: "file.pattern", Reason: fmt.Sprintf("invalid regex pattern: %v", err)}
		}
	}

	// Validate encoding
	if config.Encoding != "" && !isValidEncoding(config.Encoding) {
		return &ConfigError{Component: "source", Field: "file.encoding", Reason: fmt.Sprintf("unsupported encoding '%s'", config.Encoding)}
	}

	return nil
}

// validateDatabaseSource validates database source configuration
func (v *Validator) validateDatabaseSource(config *DatabaseSourceConfig) error {
	if config == nil {
		return &ConfigError{Component: "source", Field: "database", Reason: "database source config is required when type is 'database'"}
	}

	if strings.TrimSpace(config.ConnectionString) == "" {
		return &ConfigError{Component: "source", Field: "database.connection_string", Reason: "connection string is required"}
	}

	if strings.TrimSpace(config.Query) == "" {
		return &ConfigError{Component: "source", Field: "database.query", Reason: "query is required"}
	}

	// Validate poll interval if provided
	if config.PollInterval < 0 {
		return &ConfigError{Component: "source", Field: "database.poll_interval", Reason: "poll interval cannot be negative"}
	}

	return nil
}

// ValidateConverterConfig validates converter configuration
func (v *Validator) ValidateConverterConfig(config *ConverterConfig) error {
	if config == nil {
		return nil // Converter config is optional
	}

	if config.SchemaValidator != nil {
		if err := v.validateSchemaValidator(config.SchemaValidator); err != nil {
			return err
		}
	}

	if config.FieldMapper != nil {
		if err := v.validateFieldMapper(config.FieldMapper); err != nil {
			return err
		}
	}

	if config.RuleEngine != nil {
		if err := v.validateRuleEngine(config.RuleEngine); err != nil {
			return err
		}
	}

	return nil
}

// validateSchemaValidator validates schema validator configuration
func (v *Validator) validateSchemaValidator(config *SchemaValidatorConfig) error {
	if len(config.InputSchema) > 0 {
		// Validate input schema is valid JSON
		var schema interface{}
		if err := json.Unmarshal(config.InputSchema, &schema); err != nil {
			return &ConfigError{Component: "converter", Field: "schema_validator.input_schema", Reason: fmt.Sprintf("invalid JSON: %v", err)}
		}

		// Create a temporary compiler and add the schema
		c := jsonschema.NewCompiler()
		if err := c.AddResource("input_schema.json", strings.NewReader(string(config.InputSchema))); err != nil {
			return &ConfigError{Component: "converter", Field: "schema_validator.input_schema", Reason: fmt.Sprintf("invalid JSON schema: %v", err)}
		}
		if _, err := c.Compile("input_schema.json"); err != nil {
			return &ConfigError{Component: "converter", Field: "schema_validator.input_schema", Reason: fmt.Sprintf("schema compilation failed: %v", err)}
		}
	}

	if len(config.OutputSchema) > 0 {
		// Validate output schema is valid JSON
		var schema interface{}
		if err := json.Unmarshal(config.OutputSchema, &schema); err != nil {
			return &ConfigError{Component: "converter", Field: "schema_validator.output_schema", Reason: fmt.Sprintf("invalid JSON: %v", err)}
		}

		// Create a temporary compiler and add the schema
		c := jsonschema.NewCompiler()
		if err := c.AddResource("output_schema.json", strings.NewReader(string(config.OutputSchema))); err != nil {
			return &ConfigError{Component: "converter", Field: "schema_validator.output_schema", Reason: fmt.Sprintf("invalid JSON schema: %v", err)}
		}
		if _, err := c.Compile("output_schema.json"); err != nil {
			return &ConfigError{Component: "converter", Field: "schema_validator.output_schema", Reason: fmt.Sprintf("schema compilation failed: %v", err)}
		}
	}

	return nil
}

// validateFieldMapper validates field mapper configuration
func (v *Validator) validateFieldMapper(config *FieldMapperConfig) error {
	if len(config.Mappings) == 0 {
		return &ConfigError{Component: "converter", Field: "field_mapper.mappings", Reason: "at least one mapping is required"}
	}

	// Validate no empty keys or values
	for key, value := range config.Mappings {
		if strings.TrimSpace(key) == "" {
			return &ConfigError{Component: "converter", Field: "field_mapper.mappings", Reason: "mapping source field cannot be empty"}
		}
		if strings.TrimSpace(value) == "" {
			return &ConfigError{Component: "converter", Field: "field_mapper.mappings", Reason: fmt.Sprintf("mapping destination field for '%s' cannot be empty", key)}
		}
	}

	return nil
}

// validateRuleEngine validates rule engine configuration
func (v *Validator) validateRuleEngine(config *RuleEngineConfig) error {
	if len(config.Rules) == 0 {
		return &ConfigError{Component: "converter", Field: "rule_engine.rules", Reason: "at least one rule is required"}
	}

	for i, rule := range config.Rules {
		if strings.TrimSpace(rule.Name) == "" {
			return &ConfigError{Component: "converter", Field: fmt.Sprintf("rule_engine.rules[%d].name", i), Reason: "rule name is required"}
		}
		if strings.TrimSpace(rule.Condition) == "" {
			return &ConfigError{Component: "converter", Field: fmt.Sprintf("rule_engine.rules[%d].condition", i), Reason: "rule condition is required"}
		}
		if strings.TrimSpace(rule.Transformation) == "" {
			return &ConfigError{Component: "converter", Field: fmt.Sprintf("rule_engine.rules[%d].transformation", i), Reason: "rule transformation is required"}
		}
	}

	return nil
}

// ValidateFilterConfig validates filter configuration
func (v *Validator) ValidateFilterConfig(config *FilterConfig) error {
	if config == nil {
		return nil // Filter config is optional
	}

	if len(config.Rules) > 0 {
		for i, rule := range config.Rules {
			if strings.TrimSpace(rule.Name) == "" {
				return &ConfigError{Component: "filter", Field: fmt.Sprintf("rules[%d].name", i), Reason: "rule name is required"}
			}
			if strings.TrimSpace(rule.Condition) == "" {
				return &ConfigError{Component: "filter", Field: fmt.Sprintf("rules[%d].condition", i), Reason: "rule condition is required"}
			}
		}
	}

	if config.WASM != nil && len(config.WASM.Binary) > 0 {
		if err := v.validateWASM(config.WASM); err != nil {
			return err
		}
	}

	return nil
}

// validateWASM validates WASM binary
func (v *Validator) validateWASM(config *WASMConfig) error {
	if len(config.Binary) == 0 {
		return &ConfigError{Component: "filter", Field: "wasm.binary", Reason: "WASM binary cannot be empty"}
	}

	// Basic WASM magic number validation (0x00 0x61 0x73 0x6d = \0asm)
	if len(config.Binary) < 4 || config.Binary[0] != 0x00 || config.Binary[1] != 0x61 || config.Binary[2] != 0x73 || config.Binary[3] != 0x6d {
		return &ConfigError{Component: "filter", Field: "wasm.binary", Reason: "invalid WASM binary format"}
	}

	return nil
}

// ValidateDestinationConfig validates destination configuration
func (v *Validator) ValidateDestinationConfig(config *DestinationConfig) error {
	if config == nil {
		return &ConfigError{Component: "destination", Field: "type", Reason: "destination config is required"}
	}

	if strings.TrimSpace(config.Type) == "" {
		return &ConfigError{Component: "destination", Field: "type", Reason: "type is required (http, file, or database)"}
	}

	switch config.Type {
	case "http":
		return v.validateHTTPDestination(config.HTTP)
	case "file":
		return v.validateFileDestination(config.File)
	case "database":
		return v.validateDatabaseDestination(config.Database)
	default:
		return &ConfigError{Component: "destination", Field: "type", Reason: fmt.Sprintf("invalid type '%s', must be http, file, or database", config.Type)}
	}
}

// validateHTTPDestination validates HTTP destination configuration
func (v *Validator) validateHTTPDestination(config *HTTPDestinationConfig) error {
	if config == nil {
		return &ConfigError{Component: "destination", Field: "http", Reason: "HTTP destination config is required when type is 'http'"}
	}

	if strings.TrimSpace(config.URL) == "" {
		return &ConfigError{Component: "destination", Field: "http.url", Reason: "URL is required"}
	}

	// Validate URL format
	parsedURL, err := url.Parse(config.URL)
	if err != nil {
		return &ConfigError{Component: "destination", Field: "http.url", Reason: fmt.Sprintf("invalid URL format: %v", err)}
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return &ConfigError{Component: "destination", Field: "http.url", Reason: fmt.Sprintf("URL must use http or https scheme, got '%s'", parsedURL.Scheme)}
	}

	// Validate method
	if strings.TrimSpace(config.Method) == "" {
		return &ConfigError{Component: "destination", Field: "http.method", Reason: "HTTP method is required"}
	}

	if !isValidHTTPMethod(config.Method) {
		return &ConfigError{Component: "destination", Field: "http.method", Reason: fmt.Sprintf("invalid HTTP method '%s'", config.Method)}
	}

	// Validate auth if provided
	if config.Auth != nil {
		if err := v.validateAuthConfig(config.Auth, "destination.http.auth"); err != nil {
			return err
		}
	}

	return nil
}

// validateFileDestination validates file destination configuration
func (v *Validator) validateFileDestination(config *FileDestinationConfig) error {
	if config == nil {
		return &ConfigError{Component: "destination", Field: "file", Reason: "file destination config is required when type is 'file'"}
	}

	path := strings.TrimSpace(config.Path)
	if path == "" {
		return &ConfigError{Component: "destination", Field: "file.path", Reason: "path is required"}
	}

	// The file producer requires an absolute output directory; a relative path
	// or filename is rejected at runtime and messages would be dropped (#142).
	if !strings.HasPrefix(path, "/") {
		return &ConfigError{Component: "destination", Field: "file.path", Reason: fmt.Sprintf("output path %q must be an absolute directory (e.g. /data/output), not a relative path or filename", path)}
	}
	if strings.Contains(path, "..") {
		return &ConfigError{Component: "destination", Field: "file.path", Reason: fmt.Sprintf("output path %q must not contain '..'", path)}
	}

	// Validate format
	if config.Format != "" && !isValidFileFormat(config.Format) {
		return &ConfigError{Component: "destination", Field: "file.format", Reason: fmt.Sprintf("unsupported format '%s'", config.Format)}
	}

	return nil
}

// validateDatabaseDestination validates database destination configuration
func (v *Validator) validateDatabaseDestination(config *DatabaseDestinationConfig) error {
	if config == nil {
		return &ConfigError{Component: "destination", Field: "database", Reason: "database destination config is required when type is 'database'"}
	}

	if strings.TrimSpace(config.ConnectionString) == "" {
		return &ConfigError{Component: "destination", Field: "database.connection_string", Reason: "connection string is required"}
	}

	if strings.TrimSpace(config.Query) == "" {
		return &ConfigError{Component: "destination", Field: "database.query", Reason: "query is required"}
	}

	// Validate batch size if provided
	if config.BatchSize < 0 {
		return &ConfigError{Component: "destination", Field: "database.batch_size", Reason: "batch size cannot be negative"}
	}

	return nil
}

// validateAuthConfig validates authentication configuration
func (v *Validator) validateAuthConfig(config *AuthConfig, field string) error {
	if config == nil {
		return nil
	}

	if strings.TrimSpace(config.Type) == "" {
		return &ConfigError{Component: "auth", Field: field + ".type", Reason: "auth type is required"}
	}

	switch config.Type {
	case "basic":
		if config.Basic == nil {
			return &ConfigError{Component: "auth", Field: field, Reason: "basic auth config required"}
		}
		if strings.TrimSpace(config.Basic.Username) == "" {
			return &ConfigError{Component: "auth", Field: field + ".basic.username", Reason: "username is required"}
		}
		if strings.TrimSpace(config.Basic.Password) == "" {
			return &ConfigError{Component: "auth", Field: field + ".basic.password", Reason: "password is required"}
		}

	case "bearer":
		if config.Bearer == nil {
			return &ConfigError{Component: "auth", Field: field, Reason: "bearer auth config required"}
		}
		if strings.TrimSpace(config.Bearer.Token) == "" {
			return &ConfigError{Component: "auth", Field: field + ".bearer.token", Reason: "token is required"}
		}

	case "api_key":
		if config.APIKey == nil {
			return &ConfigError{Component: "auth", Field: field, Reason: "api_key auth config required"}
		}
		if strings.TrimSpace(config.APIKey.HeaderName) == "" {
			return &ConfigError{Component: "auth", Field: field + ".api_key.header_name", Reason: "header name is required"}
		}
		if strings.TrimSpace(config.APIKey.Key) == "" {
			return &ConfigError{Component: "auth", Field: field + ".api_key.key", Reason: "key is required"}
		}

	default:
		return &ConfigError{Component: "auth", Field: field + ".type", Reason: fmt.Sprintf("invalid auth type '%s'", config.Type)}
	}

	return nil
}

// Helper functions

func isValidHTTPMethod(method string) bool {
	validMethods := map[string]bool{
		"GET":     true,
		"POST":    true,
		"PUT":     true,
		"DELETE":  true,
		"PATCH":   true,
		"HEAD":    true,
		"OPTIONS": true,
	}
	return validMethods[strings.ToUpper(method)]
}

func isValidEncoding(encoding string) bool {
	validEncodings := map[string]bool{
		"utf-8":      true,
		"UTF-8":      true,
		"latin-1":    true,
		"latin1":     true,
		"iso-8859-1": true,
		"ascii":      true,
		"ASCII":      true,
	}
	return validEncodings[encoding]
}

func isValidFileFormat(format string) bool {
	validFormats := map[string]bool{
		"json":    true,
		"csv":     true,
		"xml":     true,
		"text":    true,
		"parquet": true,
		"avro":    true,
	}
	return validFormats[strings.ToLower(format)]
}

// =============================================================================
// DAG Validation for Graph-Based Pipeline Model
// =============================================================================

// ValidateDAG validates the graph-based connection model (nodes and edges).
// Returns a DAGValidationError with all validation errors found, or nil if valid.
// This validates:
// - Exactly 1 consumer node
// - Exactly 1 producer node
// - All edges reference existing nodes
// - No circular dependencies (cycles)
// - Consumer has outgoing edges
// - Producer is reachable from consumer
// - No orphaned nodes (all nodes on path from consumer to producer)
func (v *Validator) ValidateDAG(conn *Connection) error {
	if conn == nil {
		return &BadRequestError{Message: "connection cannot be nil"}
	}

	// If no nodes, this is a legacy connection - skip DAG validation
	if len(conn.Nodes) == 0 {
		return nil
	}

	var errors []string

	// Build node ID set for quick lookup
	nodeIDs := make(map[string]*Node)
	for _, node := range conn.Nodes {
		if node != nil {
			nodeIDs[node.ID] = node
		}
	}

	// 1. Validate node counts (at least 1 consumer, 1 producer)
	consumerIDs, producerIDs, countErrors := v.validateNodeCounts(conn.Nodes)
	errors = append(errors, countErrors...)

	// 2. Validate all edges reference existing nodes
	edgeErrors := v.validateEdgesReference(nodeIDs, conn.Edges)
	errors = append(errors, edgeErrors...)

	// If we have edge reference errors or missing nodes, skip graph traversal checks
	if len(edgeErrors) > 0 || len(consumerIDs) == 0 || len(producerIDs) == 0 {
		if len(errors) > 0 {
			return &DAGValidationError{Errors: errors}
		}
		return nil
	}

	// 3. Detect cycles
	if v.hasCycle(conn.Nodes, conn.Edges) {
		errors = append(errors, (&CircularDependencyError{
			Message: "circular dependency detected: pipeline contains a cycle",
		}).Error())
	}

	// 4. Check each consumer has outgoing edges
	for _, cID := range consumerIDs {
		outgoing := v.getOutgoingEdges(cID, conn.Edges)
		if len(outgoing) == 0 {
			errors = append(errors, (&ConsumerIsolatedError{
				ConsumerID: cID,
			}).Error())
		}
	}

	// 5. Check each producer is reachable from at least one consumer
	for _, pID := range producerIDs {
		reachable := false
		for _, cID := range consumerIDs {
			if v.isReachable(cID, pID, conn.Edges) {
				reachable = true
				break
			}
		}
		if !reachable {
			errors = append(errors, (&ProducerUnreachableError{
				ConsumerID: consumerIDs[0],
				ProducerID: pID,
			}).Error())
		}
	}

	// 6. Find orphaned nodes (nodes not on any consumer→producer path)
	reachableFromConsumers := make(map[string]bool)
	for _, cID := range consumerIDs {
		visited := make(map[string]bool)
		queue := []string{cID}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if visited[cur] {
				continue
			}
			visited[cur] = true
			reachableFromConsumers[cur] = true
			for _, e := range conn.Edges {
				if e != nil && e.Source == cur {
					queue = append(queue, e.Target)
				}
			}
		}
	}
	canReachProducers := make(map[string]bool)
	for _, pID := range producerIDs {
		visited := make(map[string]bool)
		queue := []string{pID}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if visited[cur] {
				continue
			}
			visited[cur] = true
			canReachProducers[cur] = true
			for _, e := range conn.Edges {
				if e != nil && e.Target == cur {
					queue = append(queue, e.Source)
				}
			}
		}
	}
	var orphaned []string
	for _, node := range conn.Nodes {
		if node != nil && (!reachableFromConsumers[node.ID] || !canReachProducers[node.ID]) {
			orphaned = append(orphaned, node.ID)
		}
	}
	if len(orphaned) > 0 {
		errors = append(errors, (&OrphanedNodesError{
			Nodes: orphaned,
		}).Error())
	}

	// 7. Per-node config sanity that the structural checks don't cover.
	// A file producer's output path must be an absolute directory: the worker
	// rejects relative paths / filenames at runtime and would silently drop
	// every message, so we catch it here at create/deploy (#142).
	for _, node := range conn.Nodes {
		if node == nil || node.Type != "producer" {
			continue
		}
		if msg := validateFileProducerPath(node); msg != "" {
			errors = append(errors, msg)
		}
	}

	if len(errors) > 0 {
		return &DAGValidationError{Errors: errors}
	}

	return nil
}

// validateFileProducerPath returns a non-empty error string if a producer node
// is a file sink with an invalid output path. An empty path is allowed (the
// worker falls back to its default mounted output dir); a non-empty path must
// be absolute and free of ".." traversal, matching the worker's runtime guard
// (#142). Non-file producers and unparseable config are ignored here.
func validateFileProducerPath(node *Node) string {
	var nc struct {
		Type string `json:"type"`
		File struct {
			Path string `json:"path"`
		} `json:"file"`
	}
	if len(node.Config) == 0 || json.Unmarshal(node.Config, &nc) != nil {
		return ""
	}
	// A file producer is identified the same way the worker does: type "file"
	// or a non-empty file.path.
	if nc.Type != "file" && nc.File.Path == "" {
		return ""
	}
	path := strings.TrimSpace(nc.File.Path)
	if path == "" {
		return "" // worker uses its default output dir
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Sprintf("node %s: file output path %q must be an absolute directory (e.g. /data/output) — a relative path or filename is rejected at runtime and messages would be dropped", node.ID, path)
	}
	if strings.Contains(path, "..") {
		return fmt.Sprintf("node %s: file output path %q must not contain '..'", node.ID, path)
	}
	return ""
}

// validateNodeCounts validates that there is at least 1 consumer and 1 producer.
// Returns the consumer IDs, producer IDs, and any errors found.
func (v *Validator) validateNodeCounts(nodes []*Node) (consumerIDs, producerIDs []string, errors []string) {
	for _, node := range nodes {
		if node == nil {
			continue
		}
		switch node.Type {
		case "consumer", "input":
			consumerIDs = append(consumerIDs, node.ID)
		case "producer", "output":
			producerIDs = append(producerIDs, node.ID)
		}
	}

	if len(consumerIDs) == 0 {
		errors = append(errors, (&ConsumerCountError{
			Found:    0,
			Expected: 1,
		}).Error())
	}

	if len(producerIDs) == 0 {
		errors = append(errors, (&ProducerCountError{
			Found:    0,
			Expected: 1,
		}).Error())
	}

	return consumerIDs, producerIDs, errors
}

// validateEdgesReference validates that all edges reference existing nodes.
func (v *Validator) validateEdgesReference(nodeIDs map[string]*Node, edges []*Edge) []string {
	var errors []string

	for _, edge := range edges {
		if edge == nil {
			continue
		}
		if _, exists := nodeIDs[edge.Source]; !exists {
			errors = append(errors, (&InvalidEdgeError{
				EdgeID:     edge.ID,
				InvalidRef: edge.Source,
				RefType:    "source",
			}).Error())
		}
		if _, exists := nodeIDs[edge.Target]; !exists {
			errors = append(errors, (&InvalidEdgeError{
				EdgeID:     edge.ID,
				InvalidRef: edge.Target,
				RefType:    "target",
			}).Error())
		}
	}

	return errors
}

// hasCycle detects if the graph contains a cycle using DFS with 3-state coloring.
// WHITE (0) = unvisited, GRAY (1) = in progress, BLACK (2) = finished
func (v *Validator) hasCycle(nodes []*Node, edges []*Edge) bool {
	const (
		WHITE = 0
		GRAY  = 1
		BLACK = 2
	)

	// Build adjacency list
	adj := make(map[string][]string)
	for _, edge := range edges {
		if edge != nil {
			adj[edge.Source] = append(adj[edge.Source], edge.Target)
		}
	}

	color := make(map[string]int)
	for _, node := range nodes {
		if node != nil {
			color[node.ID] = WHITE
		}
	}

	var dfs func(nodeID string) bool
	dfs = func(nodeID string) bool {
		color[nodeID] = GRAY

		for _, neighbor := range adj[nodeID] {
			if color[neighbor] == GRAY {
				// Found a back edge - cycle detected
				return true
			}
			if color[neighbor] == WHITE {
				if dfs(neighbor) {
					return true
				}
			}
		}

		color[nodeID] = BLACK
		return false
	}

	// Run DFS from each unvisited node
	for _, node := range nodes {
		if node != nil && color[node.ID] == WHITE {
			if dfs(node.ID) {
				return true
			}
		}
	}

	return false
}

// isReachable checks if targetID is reachable from sourceID using BFS.
func (v *Validator) isReachable(sourceID, targetID string, edges []*Edge) bool {
	if sourceID == targetID {
		return true
	}

	// Build adjacency list
	adj := make(map[string][]string)
	for _, edge := range edges {
		if edge != nil {
			adj[edge.Source] = append(adj[edge.Source], edge.Target)
		}
	}

	// BFS
	visited := make(map[string]bool)
	queue := []string{sourceID}
	visited[sourceID] = true

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, neighbor := range adj[current] {
			if neighbor == targetID {
				return true
			}
			if !visited[neighbor] {
				visited[neighbor] = true
				queue = append(queue, neighbor)
			}
		}
	}

	return false
}

// getOutgoingEdges returns all edges originating from the given node.
func (v *Validator) getOutgoingEdges(nodeID string, edges []*Edge) []*Edge {
	var outgoing []*Edge
	for _, edge := range edges {
		if edge != nil && edge.Source == nodeID {
			outgoing = append(outgoing, edge)
		}
	}
	return outgoing
}
