/**
 * The workspace's plan (plans/paid-plans.md): what it is on, when the trial
 * ends, and the one thing an owner can do about it — ask.
 */

import apiClient from './api'
import type { PlanName, PlanRequest, TenantBilling } from '@/types/models'

interface Envelope<T> { data: T }

export async function getBilling(tenantID: string): Promise<TenantBilling> {
  const resp = await apiClient.get<Envelope<TenantBilling>>(`/api/v1/tenants/${tenantID}/billing`)
  return resp.data.data
}

export async function requestPlan(tenantID: string, plan: PlanName, message: string): Promise<PlanRequest> {
  const resp = await apiClient.post<Envelope<PlanRequest>>(`/api/v1/tenants/${tenantID}/plan-requests`, { plan, message })
  return resp.data.data
}
