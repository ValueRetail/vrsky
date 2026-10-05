/**
 * The data-sharing and API-key calls go through the shared client: right
 * method/URL/body, the bearer token only when the tab has one — never
 * "Bearer null" — and the server's message on failure.
 */

import { describe, it, expect, beforeEach } from 'vitest'
import type { AxiosAdapter, InternalAxiosRequestConfig } from 'axios'
import apiClient from './api'
import * as svc from './tenantDataService'

let seen: InternalAxiosRequestConfig[] = []
let reply: { status: number; data: unknown } = { status: 200, data: {} }

const adapter: AxiosAdapter = async (config) => {
  seen.push(config)
  const response = { status: reply.status, statusText: '', data: reply.data, headers: {}, config }
  if (reply.status < 300) return response
  throw Object.assign(new Error('failed'), { isAxiosError: true, config, response })
}

beforeEach(() => {
  seen = []
  reply = { status: 200, data: {} }
  sessionStorage.clear()
  apiClient.defaults.adapter = adapter
})

describe('tenantDataService', () => {
  it('rotates the API key with a POST and returns the raw key', async () => {
    reply = { status: 200, data: { id: 'k1', raw_key: 'vrsky_ws_abc' } }
    const key = await svc.rotateApiKey('t1')
    expect(key.raw_key).toBe('vrsky_ws_abc')
    expect(seen[0].method).toBe('post')
    expect(seen[0].url).toBe('/api/v1/tenants/t1/api-key/rotate')
    expect(seen[0].withCredentials).toBe(true)
  })

  it('never sends "Bearer null": no Authorization header without a token', async () => {
    await svc.getApiKey('t1')
    expect(seen[0].headers.get('Authorization')).toBeFalsy()

    sessionStorage.setItem('vrsky_session_token', 'tok-9')
    await svc.getApiKey('t1')
    expect(seen[1].headers.get('Authorization')).toBe('Bearer tok-9')
  })

  it('sends request bodies as JSON and unwraps list envelopes', async () => {
    reply = { status: 200, data: { requests: [{ id: 'r1' }] } }
    expect(await svc.listIncomingRequests('t1')).toEqual([{ id: 'r1' }])
    expect(seen[0].url).toBe('/api/v1/tenants/t1/connection-requests/incoming')

    reply = { status: 200, data: { id: 'c1' } }
    await svc.approveRequest('t1', 'r1', { allowed_fields: ['name'] })
    expect(seen[1].method).toBe('post')
    expect(JSON.parse(String(seen[1].data))).toEqual({ allowed_fields: ['name'] })
    expect(seen[1].headers.get('Content-Type')).toContain('application/json')
  })

  it('fails with the message the server gave', async () => {
    reply = { status: 404, data: { error: 'NotFound', message: 'no API key found for this workspace' } }
    await expect(svc.getApiKey('t1')).rejects.toMatchObject({
      message: 'no API key found for this workspace',
      details: { status: 404 },
    })
  })
})
