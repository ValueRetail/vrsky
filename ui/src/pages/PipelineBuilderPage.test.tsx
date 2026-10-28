/**
 * #450 review: the first-login check decides between the wizard and the
 * builder from the connection count. GET /api/v1/connections answers
 * { data, total, limit, offset } (handler.go, ListResponse); only the HTTP
 * client and the (heavy) builder canvas are mocked.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'

vi.mock(import('@/services/api'), async (importOriginal) => ({
  ...(await importOriginal()),
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } as never,
}))
vi.mock('./PipelineBuilder', () => ({ default: () => <div>Builder canvas</div> }))

import apiClient from '@/services/api'
import { useAuthStore } from '@/store/authStore'
import { isOnboarded, markOnboarded } from '@/onboarding/state'
import type { Tenant } from '@/types/models'
import PipelineBuilderPage from './PipelineBuilderPage'

const get = vi.mocked(apiClient.get)

const tenant = { id: 'tenant-1', name: 'Production' } as Tenant

const listBody = (count: number, total: number) => ({
  data: {
    data: Array.from({ length: count }, (_, i) => ({ id: `conn-${i}`, name: `c${i}`, status: 'running' })),
    total,
    limit: 1,
    offset: 0,
  },
})

const showPage = () =>
  render(
    <MemoryRouter initialEntries={['/']}>
      <Routes>
        <Route path="/" element={<PipelineBuilderPage />} />
        <Route path="/welcome" element={<div>Welcome wizard</div>} />
      </Routes>
    </MemoryRouter>,
  )

beforeEach(() => {
  localStorage.clear()
  get.mockReset()
  useAuthStore.setState({ ...useAuthStore.getInitialState(), isAuthenticated: true, currentTenant: tenant }, true)
})

describe('PipelineBuilderPage first-login check', () => {
  it('opens the builder for a workspace that has connections, and remembers it', async () => {
    get.mockResolvedValue(listBody(1, 3) as never)
    showPage()

    expect(await screen.findByText('Builder canvas')).toBeInTheDocument()
    expect(screen.queryByText('Welcome wizard')).not.toBeInTheDocument()
    expect(get).toHaveBeenCalledWith('/api/v1/connections?limit=1&offset=0')
    expect(isOnboarded('tenant-1')).toBe(true)
  })

  it('sends a workspace with no connections to the wizard', async () => {
    get.mockResolvedValue(listBody(0, 0) as never)
    showPage()

    expect(await screen.findByText('Welcome wizard')).toBeInTheDocument()
    expect(isOnboarded('tenant-1')).toBe(false)
  })

  it('asks nothing for a workspace already onboarded', async () => {
    markOnboarded('tenant-1')
    showPage()

    expect(await screen.findByText('Builder canvas')).toBeInTheDocument()
    expect(get).not.toHaveBeenCalled()
  })

  it('falls back to the builder when the count cannot be read', async () => {
    get.mockRejectedValue(new Error('HTTP Error 500'))
    showPage()

    expect(await screen.findByText('Builder canvas')).toBeInTheDocument()
    expect(isOnboarded('tenant-1')).toBe(false)
  })
})
