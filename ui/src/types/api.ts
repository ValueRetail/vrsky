/**
 * API Response Types
 * Based on Phase 1 Management API specification
 */

export interface APIResponse<T = unknown> {
  data: T
  error?: string
  status: number
}

export interface APIError {
  code: string
  message: string
  details?: Record<string, unknown>
}

// Connection Management Responses
export interface CreateConnectionResponse {
  id: string
  tenant_id: string
  name: string
  description: string
  status: 'stopped' | 'running' | 'error'
  created_at: string
  updated_at: string
}

export interface GetConnectionResponse {
  id: string
  tenant_id: string
  name: string
  description: string
  status: 'stopped' | 'running' | 'error'
  source_config: Record<string, unknown>
  converter_config: Record<string, unknown>
  filter_config: Record<string, unknown>
  destination_config: Record<string, unknown>
  metrics: Record<string, unknown>
  created_at: string
  updated_at: string
  started_at?: string
  stopped_at?: string
  last_error?: string
}

export interface ListConnectionsResponse {
  connections: GetConnectionResponse[]
  total: number
  page: number
  page_size: number
}

export interface UpdateConnectionResponse extends GetConnectionResponse {}

export interface DeleteConnectionResponse {
  success: boolean
  message: string
}

// Control Operations
export interface StartConnectionResponse {
  id: string
  status: 'running'
  started_at: string
}

export interface StopConnectionResponse {
  id: string
  status: 'stopped'
  stopped_at: string
}

// Metrics
export interface ConnectionMetricsResponse {
  connection_id: string
  tenant_id: string
  status: 'running' | 'stopped'
  components: {
    consumer: {
      status: 'active' | 'idle' | 'error'
      messages_processed: number
      errors: number
      last_update: string
    }
    converter: {
      status: 'active' | 'idle' | 'error'
      messages_processed: number
      errors: number
      last_update: string
    }
    filter: {
      status: 'active' | 'idle' | 'error'
      messages_processed: number
      filtered_out: number
      errors: number
      last_update: string
    }
    producer: {
      status: 'active' | 'idle' | 'error'
      messages_sent: number
      errors: number
      last_update: string
    }
  }
  total_messages_in: number
  total_messages_out: number
  total_errors: number
  errors_per_second: number
  throughput_mps: number // messages per second
  last_updated: string
}

// Test Data
export interface TestMessageResponse {
  success: boolean
  message_id: string
  message: string
}

export interface AutoGeneratorStatusResponse {
  connection_id: string
  running: boolean
  rate: number // messages per second
  message_size: 'small' | 'medium' | 'large'
  total_generated: number
  started_at?: string
  stopped_at?: string
}

export interface StartGeneratorResponse {
  connection_id: string
  running: true
  rate: number
  message_size: string
  started_at: string
}

export interface StopGeneratorResponse {
  connection_id: string
  running: false
  stopped_at: string
  total_generated: number
}

// Health & Status
export interface HealthResponse {
  status: 'healthy' | 'degraded' | 'unhealthy'
  checks: {
    database: 'ok' | 'failed'
    nats: 'ok' | 'failed'
    api: 'ok' | 'failed'
  }
  timestamp: string
}

export interface ReadyResponse {
  ready: boolean
  timestamp: string
}

// Server-Sent Events
export interface MetricsStreamMessage {
  type: 'metrics'
  data: ConnectionMetricsResponse
  timestamp: string
}

export interface ConnectionStreamMessage {
  type: 'connection_update'
  data: GetConnectionResponse
  timestamp: string
}

export type StreamMessage = MetricsStreamMessage | ConnectionStreamMessage
