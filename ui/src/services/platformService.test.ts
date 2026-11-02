/**
 * The operator pages work for an operator with no active workspace (#637):
 * the calls go out without X-Tenant-ID, which /api/v1/platform/* accepts.
 */

import { describe, it, expect, afterEach } from 'vitest'
import type { AxiosAdapter } from 'axios'
import apiClient, { setActiveTenantId } from './api'
import { listTenants, listPlanRequests, setPlan } from './platformService'

const original = apiClient.defaults.adapter
const seen: { url?: string; tenant: unknown }[] = []

function capture() {
  seen.length = 0
  const adapter: AxiosAdapter = async (config) => {
    seen.push({ url: config.url, tenant: config.headers['X-Tenant-ID'] })
    return { status: 200, statusText: 'OK', data: { data: [] }, headers: {}, config }
  }
  apiClient.defaults.adapter = adapter
}

afterEach(() => {
  apiClient.defaults.adapter = original
  setActiveTenantId('')
})

describe('platformService with no workspace selected', () => {
  it('sends no X-Tenant-ID on any platform call', async () => {
    capture()
    setActiveTenantId('')
    await listTenants()
    await listPlanRequests()
    await setPlan('t-1', { plan: 'paid' } as never)
    expect(seen.map((s) => s.url)).toEqual([
      '/api/v1/platform/tenants',
      '/api/v1/platform/plan-requests',
      '/api/v1/platform/tenants/t-1/plan',
    ])
    expect(seen.every((s) => s.tenant === undefined)).toBe(true)
  })

  it('still sends the workspace when one is selected', async () => {
    capture()
    setActiveTenantId('t-9')
    await listTenants()
    expect(seen[0].tenant).toBe('t-9')
  })
})
