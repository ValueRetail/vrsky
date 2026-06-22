/**
 * Per-tenant usage metering service — Phase 4A (#92).
 *
 * Reads current-month (or a custom range) message/deploy/storage usage and
 * builds the CSV-export URL. The export is fetched as a blob via apiClient (not
 * window.open) so the X-Tenant-ID header from the axios interceptor is attached.
 */

import apiClient from './api'

export interface UsageDaily {
  day: string
  messages_published: number
  deploys: number
  storage_bytes: number
}

export interface UsageTotals {
  messages_published: number
  deploys: number
  storage_bytes: number
}

export interface UsageResponse {
  from: string
  to: string
  month: UsageTotals
  daily: UsageDaily[]
}

interface Envelope<T> { data: T }

function rangeParams(from?: string, to?: string): string {
  const p = new URLSearchParams()
  if (from) p.set('from', from)
  if (to) p.set('to', to)
  const s = p.toString()
  return s ? `?${s}` : ''
}

export async function getUsage(tenantID: string, from?: string, to?: string): Promise<UsageResponse> {
  const resp = await apiClient.get<Envelope<UsageResponse>>(
    `/api/v1/tenants/${tenantID}/usage${rangeParams(from, to)}`,
  )
  return resp.data.data
}

export function usageExportURL(tenantID: string, from?: string, to?: string): string {
  const p = new URLSearchParams({ format: 'csv' })
  if (from) p.set('from', from)
  if (to) p.set('to', to)
  return `/api/v1/tenants/${tenantID}/usage/export?${p.toString()}`
}
