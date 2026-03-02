/**
 * Domain Models
 * Core data structures for the application
 */

export type ConnectionStatus = 'running' | 'stopped' | 'error'

export type SourceType = 'http' | 'file' | 'database'
export type ConverterType = 'schema' | 'mapper' | 'rules'
export type FilterType = 'rules' | 'wasm'
export type DestinationType = 'http' | 'file' | 'database'

// Source Configurations
export interface HttpSourceConfig {
  type: 'http'
  url: string
  method: 'GET' | 'POST' | 'PUT' | 'DELETE'
  headers?: Record<string, string>
  auth?: {
    type: 'bearer' | 'basic'
    credentials: string
  }
  timeout?: number
  retry?: {
    max_attempts: number
    backoff_ms: number
  }
}

export interface FileSourceConfig {
  type: 'file'
  path: string
  format: 'json' | 'csv' | 'xml'
  encoding?: string
  watch?: boolean
  poll_interval_ms?: number
}

export interface DatabaseSourceConfig {
  type: 'database'
  connection_string: string
  query: string
  polling_interval_ms?: number
}

export type SourceConfig = HttpSourceConfig | FileSourceConfig | DatabaseSourceConfig

// Converter Configurations
export interface SchemaValidatorConfig {
  type: 'schema'
  input_schema: Record<string, unknown>
  validation_rules?: Record<string, unknown>
}

export interface FieldMapperConfig {
  type: 'mapper'
  mappings: Array<{
    source_field: string
    target_field: string
    transform?: string
  }>
}

export interface RuleEngineConfig {
  type: 'rules'
  rules: Array<{
    condition: string
    action: string
  }>
}

export type ConverterConfig = SchemaValidatorConfig | FieldMapperConfig | RuleEngineConfig

// Filter Configurations
export interface FilterRulesConfig {
  type: 'rules'
  rules: Array<{
    field: string
    operator: 'eq' | 'ne' | 'gt' | 'lt' | 'in' | 'nin'
    value: unknown
  }>
}

export interface WasmScriptConfig {
  type: 'wasm'
  script: string
  params?: Record<string, unknown>
}

export type FilterConfig = FilterRulesConfig | WasmScriptConfig

// Destination Configurations
export interface HttpDestinationConfig {
  type: 'http'
  url: string
  method: 'GET' | 'POST' | 'PUT' | 'DELETE'
  headers?: Record<string, string>
  auth?: {
    type: 'bearer' | 'basic'
    credentials: string
  }
  timeout?: number
  retry?: {
    max_attempts: number
    backoff_ms: number
  }
}

export interface FileDestinationConfig {
  type: 'file'
  path: string
  format: 'json' | 'csv' | 'xml'
  encoding?: string
  append?: boolean
}

export interface DatabaseDestinationConfig {
  type: 'database'
  connection_string: string
  table: string
  operation: 'insert' | 'update' | 'upsert'
}

export type DestinationConfig = HttpDestinationConfig | FileDestinationConfig | DatabaseDestinationConfig

// Connection Model
export interface Connection {
  id: string
  tenant_id: string
  name: string
  description: string
  status: ConnectionStatus
  source_config: SourceConfig
  converter_config: ConverterConfig
  filter_config: FilterConfig
  destination_config: DestinationConfig
  metrics?: ConnectionMetrics
  created_at: string
  updated_at: string
  started_at?: string
  stopped_at?: string
  last_error?: string
}

// Metrics Model
export interface ComponentMetrics {
  status: 'active' | 'idle' | 'error'
  messages_processed: number
  errors: number
  last_update: string
}

export interface FilterComponentMetrics extends ComponentMetrics {
  filtered_out: number
}

export interface ProducerComponentMetrics extends ComponentMetrics {
  messages_sent: number
}

export interface ConnectionMetrics {
  connection_id: string
  tenant_id: string
  status: ConnectionStatus
  components: {
    consumer: ComponentMetrics
    converter: ComponentMetrics
    filter: FilterComponentMetrics
    producer: ProducerComponentMetrics
  }
  total_messages_in: number
  total_messages_out: number
  total_errors: number
  errors_per_second: number
  throughput_mps: number
  last_updated: string
}

// Tenant
export interface Tenant {
  id: string
  name: string
  created_at: string
  updated_at: string
}

// Pagination
export interface PageInfo {
  page: number
  page_size: number
  total: number
  total_pages: number
}

// Notification
export interface Notification {
  id: string
  type: 'success' | 'error' | 'warning' | 'info'
  title: string
  message: string
  duration?: number
  action?: {
    label: string
    onClick: () => void
  }
}

// Confirm Dialog
export interface ConfirmDialogConfig {
  title: string
  message: string
  confirmLabel?: string
  cancelLabel?: string
  destructive?: boolean
  onConfirm: () => void | Promise<void>
  onCancel?: () => void
}
