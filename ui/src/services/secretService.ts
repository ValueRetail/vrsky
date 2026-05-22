/**
 * Secret service — Phase 1A (#66).
 *
 * Connector configs reference per-tenant secrets by UUID. The Management API
 * encrypts plaintext server-side; this service never echoes plaintext back.
 */

import apiClient from './api'

export interface Secret {
  id: string
  name: string
  created_at: string
  updated_at: string
  rotated_at?: string | null
}

interface Envelope<T> {
  data: T
}

export async function listSecrets(): Promise<Secret[]> {
  const resp = await apiClient.get<Envelope<Secret[]>>('/api/v1/secrets')
  return resp.data.data ?? []
}

export async function getSecret(id: string): Promise<Secret> {
  const resp = await apiClient.get<Envelope<Secret>>(`/api/v1/secrets/${id}`)
  return resp.data.data
}

export async function createSecret(name: string, value: string): Promise<Secret> {
  const resp = await apiClient.post<Envelope<Secret>>('/api/v1/secrets', { name, value })
  return resp.data.data
}

export async function updateSecret(
  id: string,
  changes: { name?: string; value?: string }
): Promise<Secret> {
  const resp = await apiClient.put<Envelope<Secret>>(`/api/v1/secrets/${id}`, changes)
  return resp.data.data
}

export async function rotateSecret(id: string): Promise<Secret> {
  const resp = await apiClient.post<Envelope<Secret>>(`/api/v1/secrets/${id}/rotate`)
  return resp.data.data
}

export async function deleteSecret(id: string): Promise<void> {
  await apiClient.delete(`/api/v1/secrets/${id}`)
}
