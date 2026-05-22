/**
 * Audit log service — Phase 1G (#72).
 */

import apiClient from './api'

export interface AuditEntry {
  id: string
  tenant_id: string
  user_id?: string
  actor_kind: string
  actor_label?: string
  action: string
  resource_type: string
  resource_id?: string
  method: string
  path: string
  status_code: number
  request_id?: string
  ip_address?: string
  user_agent?: string
  details?: Record<string, unknown>
  occurred_at: string
}

export interface AuditFilters {
  action?: string
  resource_type?: string
  resource_id?: string
  user_id?: string
  since?: string
  until?: string
}

export interface ListAuditResponse {
  data: AuditEntry[]
  total: number
  limit: number
  offset: number
}

function toParams(f: AuditFilters, page?: number, pageSize?: number): URLSearchParams {
  const params = new URLSearchParams()
  if (f.action) params.set('action', f.action)
  if (f.resource_type) params.set('resource_type', f.resource_type)
  if (f.resource_id) params.set('resource_id', f.resource_id)
  if (f.user_id) params.set('user_id', f.user_id)
  if (f.since) params.set('since', f.since)
  if (f.until) params.set('until', f.until)
  const limit = pageSize || 50
  const offset = page !== undefined ? (page - 1) * limit : 0
  params.set('limit', String(limit))
  params.set('offset', String(offset))
  return params
}

export async function listAudit(
  filters: AuditFilters = {},
  page = 1,
  pageSize = 50,
): Promise<ListAuditResponse> {
  const resp = await apiClient.get<ListAuditResponse>(
    `/api/v1/audit?${toParams(filters, page, pageSize).toString()}`,
  )
  return resp.data
}

/** Returns the URL the user should open to download the JSONL export. */
export function auditExportURL(filters: AuditFilters = {}): string {
  const params = toParams(filters)
  params.set('format', 'jsonl')
  return `/api/v1/audit?${params.toString()}`
}
