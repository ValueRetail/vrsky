/**
 * Tenant Data Sharing Service
 * API calls for connection requests, data connections, API keys, and audit log
 */

import { config } from '@/config/env'
import * as authService from '@/services/authService'
import type {
  DataConnectionRequest,
  TenantDataConnection,
  TenantAPIKey,
  DataAccessLogEntry,
  PageInfo,
} from '@/types/models'

function authHeaders() {
  const token = authService.getSessionToken()
  return {
    'Content-Type': 'application/json',
    'Authorization': `Bearer ${token}`,
  }
}

async function fetchJSON<T>(url: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${config.apiUrl}${url}`, {
    headers: authHeaders(),
    ...options,
  })
  if (!res.ok) {
    const data = await res.json().catch(() => ({}))
    throw new Error(data.message || `HTTP ${res.status}`)
  }
  return res.json()
}

// Connection requests
export async function createConnectionRequest(
  tenantId: string,
  body: { target_tenant_id?: string; target_api_key?: string; permission_type: string; message?: string }
): Promise<DataConnectionRequest> {
  return fetchJSON(`/api/v1/tenants/${tenantId}/connection-requests`, {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

export async function listIncomingRequests(tenantId: string): Promise<DataConnectionRequest[]> {
  const data = await fetchJSON<{ requests: DataConnectionRequest[] }>(
    `/api/v1/tenants/${tenantId}/connection-requests/incoming`
  )
  return data.requests
}

export async function listOutgoingRequests(tenantId: string): Promise<DataConnectionRequest[]> {
  const data = await fetchJSON<{ requests: DataConnectionRequest[] }>(
    `/api/v1/tenants/${tenantId}/connection-requests/outgoing`
  )
  return data.requests
}

export async function approveRequest(
  tenantId: string,
  requestId: string,
  fields?: { allowed_fields?: string[]; denied_fields?: string[]; shared_connection_ids?: string[] }
): Promise<TenantDataConnection> {
  return fetchJSON(`/api/v1/tenants/${tenantId}/connection-requests/${requestId}/approve`, {
    method: 'POST',
    body: JSON.stringify(fields || {}),
  })
}

export async function denyRequest(tenantId: string, requestId: string): Promise<void> {
  await fetchJSON(`/api/v1/tenants/${tenantId}/connection-requests/${requestId}/deny`, {
    method: 'POST',
  })
}

// Active data connections
export async function listDataConnections(tenantId: string): Promise<TenantDataConnection[]> {
  const data = await fetchJSON<{ connections: TenantDataConnection[] }>(
    `/api/v1/tenants/${tenantId}/data-connections`
  )
  return data.connections
}

export async function getDataConnection(tenantId: string, connectionId: string): Promise<TenantDataConnection> {
  return fetchJSON(`/api/v1/tenants/${tenantId}/data-connections/${connectionId}`)
}

export async function revokeDataConnection(tenantId: string, connectionId: string): Promise<void> {
  await fetchJSON(`/api/v1/tenants/${tenantId}/data-connections/${connectionId}/revoke`, {
    method: 'POST',
  })
}

// Shared connections (what a target tenant has shared with requester)
export async function getSharedConnections(
  tenantId: string,
  dataConnectionId: string
): Promise<{ id: string; name: string }[]> {
  const data = await fetchJSON<{ shared_connections: { id: string; name: string }[] }>(
    `/api/v1/tenants/${tenantId}/data-connections/${dataConnectionId}/shared-connections`
  )
  return data.shared_connections
}

// Audit log
export async function getDataAccessLog(
  tenantId: string,
  page = 1,
  pageSize = 20
): Promise<{ entries: DataAccessLogEntry[]; page_info: PageInfo }> {
  return fetchJSON(
    `/api/v1/tenants/${tenantId}/data-access-log?page=${page}&page_size=${pageSize}`
  )
}

// API key management (wrapping existing Phase 2 endpoints)
export async function getApiKey(tenantId: string): Promise<TenantAPIKey> {
  return fetchJSON(`/api/v1/tenants/${tenantId}/api-key`)
}

export async function rotateApiKey(tenantId: string): Promise<TenantAPIKey & { raw_key: string }> {
  return fetchJSON(`/api/v1/tenants/${tenantId}/api-key/rotate`, {
    method: 'POST',
  })
}
