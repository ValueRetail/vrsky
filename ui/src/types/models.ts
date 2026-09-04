/**
 * Domain Models
 * Core data structures for the application
 */

export type ConnectionStatus = 'running' | 'stopped' | 'error'

// Graph pipeline model (nodes/edges) — the only connection format.
export interface ConnectionNode {
  id: string
  type: string
  config?: Record<string, unknown>
  enabled?: boolean
}

export interface ConnectionEdge {
  id?: string
  source: string
  target: string
  order?: number
}

// Connection Model
export interface Connection {
  id: string
  tenant_id: string
  name: string
  description: string
  status: ConnectionStatus
  nodes?: ConnectionNode[]
  edges?: ConnectionEdge[]
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
