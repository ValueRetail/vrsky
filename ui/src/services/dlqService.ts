/**
 * Dead-letter queue service — Phase 1E (#70).
 *
 * Lists messages that exhausted JetStream's MaxDeliver budget and offers
 * retry / discard operations. Server-side ownership checks ensure tenants
 * only see their own DLQ entries.
 */

import apiClient from './api'

export interface DLQEntry {
  sequence: number
  subject: string
  tenant_id: string
  connection_id: string
  worker: string
  last_error: string
  delivered: number
  received_at: string
  payload_size: number
  payload?: unknown
  headers?: Record<string, string>
}

interface Envelope<T> {
  data: T
}

export async function listDLQ(connectionID: string, limit = 50, offset = 0): Promise<DLQEntry[]> {
  const params = new URLSearchParams({ limit: String(limit), offset: String(offset) })
  const resp = await apiClient.get<Envelope<DLQEntry[]>>(
    `/api/v1/connections/${connectionID}/dlq?${params.toString()}`,
  )
  return resp.data.data ?? []
}

export async function getDLQEntry(connectionID: string, seq: number): Promise<DLQEntry> {
  const resp = await apiClient.get<Envelope<DLQEntry>>(
    `/api/v1/connections/${connectionID}/dlq/${seq}`,
  )
  return resp.data.data
}

export async function retryDLQ(connectionID: string, seq: number): Promise<void> {
  await apiClient.post(`/api/v1/connections/${connectionID}/dlq/${seq}/retry`)
}

export async function discardDLQ(connectionID: string, seq: number): Promise<void> {
  await apiClient.post(`/api/v1/connections/${connectionID}/dlq/${seq}/discard`)
}
