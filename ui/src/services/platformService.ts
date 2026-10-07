/**
 * The platform operator's side of paid plans (plans/paid-plans.md). Every
 * route here is refused for anyone not in PLATFORM_OPERATORS; the UI only shows
 * the page to users /auth/me marks as operators.
 */

import apiClient from './api'
import type { PlanName, PlanRequest, PlatformTenant } from '@/types/models'

interface Envelope<T> { data: T }

export interface PlanChange {
  plan: PlanName
  trial_ends_at?: string
  note?: string
}

export interface PlanChangeResult {
  tenant_id: string
  plan: PlanName
  billing_status: string
  trial_ends_at?: string
  pipelines_resumed: number
}

export async function listTenants(): Promise<PlatformTenant[]> {
  const resp = await apiClient.get<Envelope<PlatformTenant[]>>('/api/v1/platform/tenants')
  return resp.data.data
}

export async function listPlanRequests(): Promise<PlanRequest[]> {
  const resp = await apiClient.get<Envelope<PlanRequest[]>>('/api/v1/platform/plan-requests')
  return resp.data.data
}

export async function setPlan(tenantID: string, change: PlanChange): Promise<PlanChangeResult> {
  const resp = await apiClient.put<Envelope<PlanChangeResult>>(`/api/v1/platform/tenants/${tenantID}/plan`, change)
  return resp.data.data
}
