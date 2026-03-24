/**
 * Domain Models
 * Core data structures for the application
 */

export type ConnectionStatus = 'running' | 'stopped' | 'error'

export type SourceType = 'http' | 'file' | 'database' | 'api' | 'tenant'
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

// API Consumer Source Configuration
export interface ApiConsumerEndpoint {
  path: string
  auth_type: 'none' | 'bearer' | 'api_key'
  auth_value: string
}

export interface ApiConsumerSourceConfig {
  type: 'api'
  base_url: string
  endpoints: ApiConsumerEndpoint[]
  poll_interval_seconds: number
}

export type SourceConfig = HttpSourceConfig | FileSourceConfig | DatabaseSourceConfig | ApiConsumerSourceConfig

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
export type TenantRole = 'owner' | 'admin' | 'editor' | 'viewer'

export type TenantStatus = 'provisioning' | 'active' | 'failed' | 'terminating'

export interface Tenant {
  id: string
  name: string
  slug: string
  owner_id: string
  subscription_plan: string
  is_verified: boolean
  user_role: TenantRole
  max_integrations: number
  max_messages_per_month: number
  status: TenantStatus
  nats_slug?: string
  created_at: string
  updated_at: string
}

export interface ProvisioningStatus {
  tenant_id: string
  status: TenantStatus
  progress: number
  current_step: string
  nats_url?: string
  error?: string
}

export interface TenantAPIKey {
  id: string
  tenant_id: string
  created_at: string
  rotated_at?: string
  is_active: boolean
  raw_key?: string
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

// ============================================
// Authentication Models (Phase 1)
// ============================================

export type UserStatus = 'pending_verification' | 'active' | 'suspended' | 'deactivated'

export interface User {
  id: string
  email: string
  full_name: string
  status: UserStatus
  email_verified: boolean
  created_at: string
  last_login_at?: string
}

export interface Session {
  token: string
  user: User
  expires_at: string
}

// Auth Request/Response types
export interface RegisterRequest {
  email: string
  password: string
  full_name: string
  workspace_name: string
}

export interface LoginRequest {
  email: string
  password: string
}

export interface ChangePasswordRequest {
  current_password: string
  new_password: string
}

export interface ForgotPasswordRequest {
  email: string
}

export interface ResetPasswordRequest {
  token: string
  new_password: string
}

export interface AuthResponse {
  success: boolean
  message?: string
  user?: User
  session_token?: string
  expires_at?: string
}

export interface MeResponse {
  user: User
  session_expires_at: string
  tenants: Tenant[]
  current_tenant: Tenant | null
}

// ============================================
// Data Sharing Models (Phase 3)
// ============================================

export type DataConnectionRequestStatus = 'pending' | 'approved' | 'denied' | 'revoked'
export type DataConnectionPermission = 'send' | 'receive' | 'both'
export type DataConnectionStatus = 'active' | 'paused' | 'revoked'

export interface DataConnectionRequest {
  id: string
  requester_tenant_id: string
  target_tenant_id: string
  permission_type: DataConnectionPermission
  status: DataConnectionRequestStatus
  message?: string
  allowed_fields?: string[]
  denied_fields?: string[]
  created_at: string
  updated_at: string
  responded_at?: string
  requester_tenant_name?: string
  target_tenant_name?: string
}

export interface TenantDataConnection {
  id: string
  request_id: string
  requester_tenant_id: string
  target_tenant_id: string
  permission_type: DataConnectionPermission
  allowed_fields?: string[]
  denied_fields?: string[]
  rate_limit_per_hour: number
  status: DataConnectionStatus
  created_at: string
  updated_at: string
  revoked_at?: string
}

export interface DataAccessLogEntry {
  id: string
  connection_id: string
  requester_tenant_id: string
  target_tenant_id: string
  request_time: string
  fields_accessed?: string[]
  bytes_received: number
  status_code: number
  ip_address?: string
}
