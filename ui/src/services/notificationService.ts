/**
 * Notification targets service (Phase 3A — #84)
 *
 * Talks to the management-api /api/v1/notifications/* endpoints. Uses the
 * shared apiClient so the X-Tenant-ID header + session cookie are attached by
 * its interceptors and requests stay same-origin via the Vite proxy.
 */

import apiClient from './api'

export type NotificationTargetType = 'slack' | 'email' | 'pagerduty' | 'webhook'

export interface NotificationTarget {
  id: string
  name: string
  type: NotificationTargetType
  email?: string
  url?: string
  platform: boolean
  min_severity?: string
  enabled: boolean
  has_secret: boolean
  created_at: string
  updated_at: string
}

export interface NotificationTargetInput {
  name: string
  type: NotificationTargetType
  secret?: string // Slack webhook URL / PagerDuty routing key / webhook HMAC key
  email?: string
  url?: string
  platform?: boolean
  min_severity?: string
  enabled?: boolean
}

export async function listTargets(): Promise<NotificationTarget[]> {
  const res = await apiClient.get<{ targets: NotificationTarget[] }>('/api/v1/notifications/targets')
  return res.data.targets ?? []
}

export async function createTarget(input: NotificationTargetInput): Promise<NotificationTarget> {
  const res = await apiClient.post<NotificationTarget>('/api/v1/notifications/targets', input)
  return res.data
}

export async function updateTarget(id: string, input: NotificationTargetInput): Promise<NotificationTarget> {
  const res = await apiClient.put<NotificationTarget>(`/api/v1/notifications/targets/${id}`, input)
  return res.data
}

export async function deleteTarget(id: string): Promise<void> {
  await apiClient.delete(`/api/v1/notifications/targets/${id}`)
}

export async function testTarget(id: string): Promise<{ ok: boolean; error?: string }> {
  const res = await apiClient.post<{ ok: boolean; error?: string }>(
    `/api/v1/notifications/targets/${id}/test`,
    {}
  )
  return res.data
}
