/**
 * The sidebar: the Connections count is the workspace total (#450), the
 * running / stopped / error cards count the loaded connections, or use the
 * API's per-status totals once the workspace outgrows one page (#541), entries
 * are gated by role, the current section is highlighted, and the docs link
 * never carries an unsafe scheme.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

vi.mock(import('@/services/api'), async (importOriginal) => ({
  ...(await importOriginal()),
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } as never,
}))

import apiClient from '@/services/api'
import { useAuthStore } from '@/store/authStore'
import { useConnectionsStore } from '@/store/connectionsStore'
import { config } from '@/config/env'
import type { Connection, Tenant, TenantRole } from '@/types/models'
import Sidebar from './Sidebar'

const docsUrl = config.docsUrl

const tenant = (role: TenantRole): Tenant => ({
  id: 'tenant-1',
  name: 'Production',
  slug: 'production',
  owner_id: 'user-1',
  subscription_plan: 'paid',
  is_verified: true,
  user_role: role,
  max_integrations: 10,
  max_messages_per_month: 100000,
  status: 'active',
  billing_status: 'paid',
  created_at: '2026-10-01T00:00:00Z',
  updated_at: '2026-10-01T00:00:00Z',
})

const conn = (id: string, status: string) => ({ id, name: id, status }) as unknown as Connection

beforeEach(() => {
  useAuthStore.setState({ ...useAuthStore.getInitialState(), currentTenant: tenant('owner') }, true)
  useConnectionsStore.setState(useConnectionsStore.getInitialState(), true)
})

afterEach(() => {
  config.docsUrl = docsUrl
})

const show = (path = '/') =>
  render(
    <MemoryRouter initialEntries={[path]}>
      <Sidebar />
    </MemoryRouter>,
  )

const connectionsLink = () => screen.getByRole('link', { name: /⚡ Connections/ })

describe('Sidebar connection counts', () => {
  it('shows the workspace total next to Connections, even beyond the loaded page', () => {
    useConnectionsStore.setState({ connections: [conn('a', 'running'), conn('b', 'running')], total: 12 })
    show()
    expect(connectionsLink()).toHaveTextContent(/Connections\s*12/)
  })

  it('shows no count and no status cards for an empty workspace', () => {
    show()
    expect(connectionsLink()).toHaveTextContent(/^⚡ Connections$/)
    expect(screen.queryByText('Running')).not.toBeInTheDocument()
    expect(screen.queryByText('Stopped')).not.toBeInTheDocument()
    expect(screen.queryByText('Errors')).not.toBeInTheDocument()
  })

  it('shows a card per status that has connections, with its count', () => {
    useConnectionsStore.setState({
      connections: [conn('a', 'running'), conn('b', 'running'), conn('c', 'stopped'), conn('d', 'error'), conn('e', 'error'), conn('f', 'error')],
      total: 6,
    })
    show()
    expect(screen.getByText('Running').parentElement!.parentElement).toHaveTextContent(/Running\s*2/)
    expect(screen.getByText('Stopped').parentElement).toHaveTextContent(/Stopped\s*1/)
    expect(screen.getByText('Errors').parentElement).toHaveTextContent(/Errors\s*3/)
  })

  it('leaves out the cards for statuses with no connections', () => {
    useConnectionsStore.setState({ connections: [conn('a', 'stopped')], total: 1 })
    show()
    expect(screen.getByText('Stopped')).toBeInTheDocument()
    expect(screen.queryByText('Running')).not.toBeInTheDocument()
    expect(screen.queryByText('Errors')).not.toBeInTheDocument()
  })
})

/**
 * #541: with more connections than one summary page holds, the cards show the
 * API's per-status totals. Driven through the real store load(); the HTTP
 * client answers like ListConnections in src/pkg/managementapi/handler.go
 * (limit 1..100 else 20, offset, a status filter its total counts too).
 */
describe('Sidebar status cards beyond the loaded page (#541)', () => {
  const get = vi.mocked(apiClient.get)

  const workspace = (running: number, stopped: number, error: number) => [
    ...Array.from({ length: running }, (_, i) => ({ id: `r${i}`, name: `r${i}`, status: 'running' })),
    ...Array.from({ length: stopped }, (_, i) => ({ id: `s${i}`, name: `s${i}`, status: 'stopped' })),
    ...Array.from({ length: error }, (_, i) => ({ id: `e${i}`, name: `e${i}`, status: 'error' })),
  ]

  const serve = (items: ReturnType<typeof workspace>, failStatus?: string) =>
    get.mockImplementation(((url: string) => {
      const u = new URL(url, 'http://api.test')
      if (u.pathname !== '/api/v1/connections') return Promise.reject(new Error(`unexpected ${url}`))
      const q = u.searchParams
      const status = q.get('status') ?? ''
      if (status && status === failStatus) return Promise.reject(new Error('HTTP Error 500'))
      const l = Number(q.get('limit'))
      const limit = Number.isInteger(l) && l > 0 && l <= 100 ? l : 20
      const o = Number(q.get('offset'))
      const offset = Number.isInteger(o) && o >= 0 ? o : 0
      const matches = status ? items.filter((c) => c.status === status) : items
      return Promise.resolve({ data: { data: matches.slice(offset, offset + limit), total: matches.length, limit, offset } })
    }) as never)

  const card = (label: string) => {
    const el = screen.getByText(label)
    // Running has a dot beside its label, so its card is one level further up.
    return label === 'Running' ? el.parentElement!.parentElement! : el.parentElement!
  }

  beforeEach(() => {
    get.mockReset()
  })

  it('250 connections, the loaded 100 all running: cards show 120 / 80 / 50 from the API', async () => {
    serve(workspace(120, 80, 50))
    show()
    await act(() => useConnectionsStore.getState().load())

    expect(connectionsLink()).toHaveTextContent(/Connections\s*250/)
    expect(card('Running')).toHaveTextContent(/^Running\s*120$/)
    expect(card('Stopped')).toHaveTextContent(/^Stopped\s*80$/)
    expect(card('Errors')).toHaveTextContent(/^Errors\s*50$/)
  })

  it('a small workspace loaded the same way still counts its loaded items, with one request', async () => {
    serve(workspace(2, 1, 3))
    show()
    await act(() => useConnectionsStore.getState().load())

    expect(get).toHaveBeenCalledTimes(1)
    expect(card('Running')).toHaveTextContent(/^Running\s*2$/)
    expect(card('Stopped')).toHaveTextContent(/^Stopped\s*1$/)
    expect(card('Errors')).toHaveTextContent(/^Errors\s*3$/)
  })

  it('a failed status request leaves no cards and no count', async () => {
    serve(workspace(120, 80, 50), 'stopped')
    show()
    await act(() => useConnectionsStore.getState().load())

    expect(connectionsLink()).toHaveTextContent(/^⚡ Connections$/)
    expect(screen.queryByText('Running')).not.toBeInTheDocument()
    expect(screen.queryByText('Stopped')).not.toBeInTheDocument()
    expect(screen.queryByText('Errors')).not.toBeInTheDocument()
  })

  it('with only connections set (no load), counts the loaded items and makes no request', () => {
    useConnectionsStore.setState({
      connections: [conn('a', 'running'), conn('b', 'error'), conn('c', 'error'), conn('d', 'stopped')],
      total: 12,
    })
    show()

    expect(get).not.toHaveBeenCalled()
    expect(card('Running')).toHaveTextContent(/^Running\s*1$/)
    expect(card('Stopped')).toHaveTextContent(/^Stopped\s*1$/)
    expect(card('Errors')).toHaveTextContent(/^Errors\s*2$/)
  })
})

describe('Sidebar entries by role', () => {
  it.each([
    ['owner', true],
    ['admin', true],
    ['editor', false],
    ['viewer', false],
  ] as const)('OAuth providers for %s: %s', (role, shown) => {
    useAuthStore.setState({ currentTenant: tenant(role) })
    show()
    expect(!!screen.queryByRole('link', { name: 'OAuth providers' })).toBe(shown)
  })

  it('hides OAuth providers with no workspace', () => {
    useAuthStore.setState({ currentTenant: null })
    show()
    expect(screen.queryByRole('link', { name: 'OAuth providers' })).not.toBeInTheDocument()
  })

  it('shows the Platform section only to a platform operator', () => {
    show()
    expect(screen.queryByRole('link', { name: 'Workspaces' })).not.toBeInTheDocument()
  })

  it('a platform operator gets the Workspaces link', () => {
    useAuthStore.setState({ isPlatformOperator: true })
    show()
    expect(screen.getByRole('link', { name: 'Workspaces' })).toHaveAttribute('href', '/platform/tenants')
  })
})

describe('Sidebar highlights the current section', () => {
  const active = 'bg-primary-100'

  it.each([
    ['/connections/abc', /⚡ Connections/],
    ['/settings/connection-requests', 'Requests'],
    ['/settings/tenant-connections', 'Data Connections'],
    ['/settings/api-key', 'API Key'],
    ['/settings/oauth', 'OAuth providers'],
    ['/settings/notifications', 'Notifications'],
    ['/settings/audit', 'Audit log'],
    ['/settings/users', 'Members'],
    ['/settings/agents', 'Remote agents'],
    ['/settings/usage', 'Usage & quotas'],
    ['/settings/plan', 'Plan'],
    ['/platform/tenants', 'Workspaces'],
  ] as const)('%s highlights its link and only that one', (path, name) => {
    useAuthStore.setState({ isPlatformOperator: true })
    show(path)
    const nav = screen.getByRole('navigation')
    const links = within(nav).getAllByRole('link').filter((a) => a.getAttribute('href')?.startsWith('/'))
    const current = within(nav).getByRole('link', { name })
    expect(current).toHaveClass(active)
    for (const link of links) {
      if (link !== current && link.getAttribute('href') !== '/') expect(link).not.toHaveClass(active)
    }
  })

  it('Home is highlighted on the home page', () => {
    show('/')
    expect(screen.getByRole('link', { name: /Home/ })).toHaveClass(active)
    expect(connectionsLink()).not.toHaveClass(active)
  })
})

describe('Sidebar docs link', () => {
  it.each([
    ['https://docs.example.test/vrsky/', 'https://docs.example.test/vrsky/'],
    ['http://docs.local/', 'http://docs.local/'],
    ['/docs/', '/docs/'],
    ['javascript:alert(1)', '#'],
    ['data:text/html,hi', '#'],
  ])('%s becomes %s', (url, href) => {
    config.docsUrl = url
    show()
    const link = screen.getByRole('link', { name: /Help & Docs/ })
    expect(link).toHaveAttribute('href', href)
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })
})

describe('Sidebar new pipeline', () => {
  it('links to the builder', () => {
    show()
    expect(screen.getByRole('link', { name: /New Pipeline/ })).toHaveAttribute('href', '/pipeline-builder')
  })
})
