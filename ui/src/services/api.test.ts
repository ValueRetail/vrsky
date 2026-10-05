/**
 * The shared client reports a 401 to services/sessionExpiry — and nothing
 * else — while still rejecting the request with the server's message.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import type { AxiosAdapter } from 'axios'

const reportUnauthorized = vi.fn(() => Promise.resolve())
vi.mock('@/services/sessionExpiry', () => ({ reportUnauthorized: () => reportUnauthorized() }))

import apiClient, { VRSkyAPIError } from './api'

function respondWith(status: number, data: unknown): AxiosAdapter {
  return async (config) => {
    const response = { status, statusText: '', data, headers: {}, config }
    if (status >= 200 && status < 300) return response
    throw Object.assign(new Error(`HTTP ${status}`), { isAxiosError: true, config, response })
  }
}

beforeEach(() => reportUnauthorized.mockClear())

describe('apiClient response interceptor', () => {
  it('reports a 401 and still rejects with the server message', async () => {
    apiClient.defaults.adapter = respondWith(401, { error: 'Unauthorized', message: 'invalid or expired session' })
    await expect(apiClient.get('/api/v1/agents')).rejects.toMatchObject({ message: 'invalid or expired session', code: 'Unauthorized' })
    expect(reportUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('does not report other failures', async () => {
    for (const status of [400, 403, 404, 500]) {
      apiClient.defaults.adapter = respondWith(status, { error: 'X', message: 'm' })
      await expect(apiClient.get('/x')).rejects.toBeInstanceOf(VRSkyAPIError)
    }
    apiClient.defaults.adapter = respondWith(200, { ok: true })
    await apiClient.get('/x')
    expect(reportUnauthorized).not.toHaveBeenCalled()
  })
})
