/**
 * The sidebar: the Connections count is the workspace total (#450), the
 * running / stopped / error cards come from the loaded connections, entries
 * are gated by role, the current section is highlighted, and the docs link
 * never carries an unsafe scheme.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

vi.mock(import('@/services/api'), async (importOriginal) => ({
  ...(await importOriginal()),
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } as never,
}))

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
