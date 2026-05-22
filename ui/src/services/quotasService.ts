/**
 * Tenant quotas service — Phase 1I (#74).
 */

import apiClient from './api'

export interface TenantQuotas {
  tenant_id: string
  plan_name: string
  max_msg_per_sec: number
  max_integrations: number
  max_storage_bytes: number
  storage_bytes: number
  storage_exceeded: boolean
  updated_at: string
}

interface Envelope<T> { data: T }

export async function getQuotas(tenantID: string): Promise<TenantQuotas> {
  const resp = await apiClient.get<Envelope<TenantQuotas>>(
    `/api/v1/tenants/${tenantID}/quotas`,
  )
  return resp.data.data
}

export async function updateQuotas(
  tenantID: string,
  patch: Partial<Omit<TenantQuotas, 'tenant_id' | 'storage_bytes' | 'storage_exceeded' | 'updated_at'>>,
): Promise<TenantQuotas> {
  const resp = await apiClient.put<Envelope<TenantQuotas>>(
    `/api/v1/tenants/${tenantID}/quotas`,
    patch,
  )
  return resp.data.data
}
