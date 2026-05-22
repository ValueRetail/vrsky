/**
 * Tenant members service — Phase 1D (#69).
 *
 * The /members endpoints sit under /api/v1/tenants/{id}, not the
 * X-Tenant-ID-header world, so we pass the tenant ID explicitly in the
 * URL rather than relying on the apiClient interceptor.
 */

import apiClient from './api'

export type TenantRole = 'owner' | 'admin' | 'editor' | 'viewer'

export interface TenantMember {
  user_id: string
  tenant_id: string
  email: string
  full_name?: string
  role: TenantRole
  invited_at: string
  joined_at?: string
}

interface Envelope<T> { data: T }

export async function listMembers(tenantID: string): Promise<TenantMember[]> {
  const resp = await apiClient.get<Envelope<TenantMember[]>>(
    `/api/v1/tenants/${tenantID}/members`,
  )
  return resp.data.data ?? []
}

export async function setMemberRole(
  tenantID: string,
  userID: string,
  role: TenantRole,
): Promise<void> {
  await apiClient.put(`/api/v1/tenants/${tenantID}/members/${userID}`, { role })
}

export async function removeMember(tenantID: string, userID: string): Promise<void> {
  await apiClient.delete(`/api/v1/tenants/${tenantID}/members/${userID}`)
}
