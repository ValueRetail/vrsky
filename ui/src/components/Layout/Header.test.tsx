/**
 * The header: sidebar toggle, the connection badge (the store's total, #450),
 * the workspace selector for a signed-in user, and the user menu (open,
 * close on an outside click, logout, delete account behind a confirmation,
 * login for a visitor). Only the HTTP client is mocked; no session token is
 * stored, so logout makes no request.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'

vi.mock(import('@/services/api'), async (importOriginal) => ({
  ...(await importOriginal()),
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } as never,
}))

import { useAuthStore } from '@/store/authStore'
import { useConnectionsStore } from '@/store/connectionsStore'
import { useUIStore } from '@/store/uiStore'
import type { Tenant, User } from '@/types/models'
import Header from './Header'

const user: User = {
  id: 'user-1',
  email: 'owner@example.test',
  full_name: 'Ada Owner',
  status: 'active',
  email_verified: true,
  created_at: '2026-10-01T00:00:00Z',
}

const tenant: Tenant = {
  id: 'tenant-1',
  name: 'Production',
  slug: 'production',
  owner_id: 'user-1',
  subscription_plan: 'paid',
  is_verified: true,
  user_role: 'owner',
  max_integrations: 10,
  max_messages_per_month: 100000,
  status: 'active',
  billing_status: 'paid',
  created_at: '2026-10-01T00:00:00Z',
  updated_at: '2026-10-01T00:00:00Z',
}

const signIn = (u: User = user) =>
  useAuthStore.setState({
    user: u,
    isAuthenticated: true,
    isInitialized: true,
    tenants: [tenant],
    currentTenant: tenant,
  })

beforeEach(() => {
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useConnectionsStore.setState(useConnectionsStore.getInitialState(), true)
  useUIStore.setState(useUIStore.getInitialState(), true)
})

const show = () =>
  render(
    <MemoryRouter initialEntries={['/home']}>
      <Routes>
        <Route path="/home" element={<><Header /><p>Home page</p></>} />
        <Route path="/login" element={<p>Login page</p>} />
      </Routes>
    </MemoryRouter>,
  )

const menuButton = () => screen.getByRole('button', { name: 'Open user menu' })

describe('Header', () => {
  it('toggles the sidebar', () => {
    show()
    expect(useUIStore.getState().sidebarOpen).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: 'Toggle navigation sidebar' }))
    expect(useUIStore.getState().sidebarOpen).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: 'Toggle navigation sidebar' }))
    expect(useUIStore.getState().sidebarOpen).toBe(true)
  })

  it('the badge shows the store total and follows it', () => {
    useConnectionsStore.setState({ total: 4 })
    show()
    const badge = screen.getByText('connections').parentElement!
    expect(badge).toHaveTextContent(/^\s*4\s*connections\s*$/)
    act(() => useConnectionsStore.setState({ total: 5 }))
    expect(badge).toHaveTextContent(/^\s*5\s*connections\s*$/)
  })

  it('shows the workspace selector and the user name only when signed in', () => {
    show()
    expect(screen.queryByRole('combobox', { name: 'Select workspace' })).not.toBeInTheDocument()
    expect(menuButton()).toHaveAttribute('title', 'Menu')
  })

  it('shows the signed-in user and their workspace', () => {
    signIn()
    show()
    expect(screen.getByRole('combobox', { name: 'Select workspace' })).toHaveValue('tenant-1')
    expect(menuButton()).toHaveAttribute('title', 'Logged in as owner@example.test')
    expect(menuButton()).toHaveTextContent('Ada Owner')
  })

  it('falls back to the email when the user has no name', () => {
    signIn({ ...user, full_name: '' })
    show()
    expect(menuButton()).toHaveTextContent('owner@example.test')
    fireEvent.click(menuButton())
    expect(screen.getByText('User')).toBeInTheDocument()
  })
})

describe('Header user menu', () => {
  it('opens with the user details and closes on a click outside', () => {
    signIn()
    show()
    expect(menuButton()).toHaveAttribute('aria-expanded', 'false')

    fireEvent.click(menuButton())
    expect(menuButton()).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getAllByText('owner@example.test').length).toBeGreaterThan(0)
    expect(screen.getByRole('button', { name: /Logout/ })).toBeInTheDocument()

    // A click inside the menu keeps it open.
    fireEvent.mouseDown(screen.getByRole('button', { name: /Logout/ }))
    expect(menuButton()).toHaveAttribute('aria-expanded', 'true')

    fireEvent.mouseDown(screen.getByText('Home page'))
    expect(menuButton()).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('button', { name: /Logout/ })).not.toBeInTheDocument()
  })

  it('closes when the menu button is clicked again', () => {
    signIn()
    show()
    fireEvent.click(menuButton())
    fireEvent.click(menuButton())
    expect(menuButton()).toHaveAttribute('aria-expanded', 'false')
  })

  it('Logout signs out and goes to the login page', async () => {
    signIn()
    show()
    fireEvent.click(menuButton())
    fireEvent.click(screen.getByRole('button', { name: /Logout/ }))

    expect(await screen.findByText('Login page')).toBeInTheDocument()
    expect(useAuthStore.getState().isAuthenticated).toBe(false)
    expect(useAuthStore.getState().user).toBeNull()
  })

  it('Delete Account asks for a destructive confirmation before doing anything', async () => {
    signIn()
    show()
    fireEvent.click(menuButton())
    fireEvent.click(screen.getByRole('button', { name: 'Delete Account' }))

    // The menu closes and the confirmation is up; nothing has happened yet.
    expect(menuButton()).toHaveAttribute('aria-expanded', 'false')
    const dialog = useUIStore.getState().confirmDialog!
    expect(dialog).toMatchObject({ title: 'Delete Account', confirmLabel: 'Delete Account', destructive: true })
    expect(dialog.message).toMatch(/permanently delete your account/)
    expect(useAuthStore.getState().isAuthenticated).toBe(true)

    // Without a session the delete is refused; the user stays where they are.
    await act(async () => {
      await dialog.onConfirm()
    })
    expect(useUIStore.getState().confirmDialog).toBeNull()
    expect(screen.getByText('Home page')).toBeInTheDocument()
  })

  it('offers Login to a visitor', async () => {
    show()
    fireEvent.click(menuButton())
    expect(screen.queryByRole('button', { name: /Logout/ })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /Login/ }))
    await waitFor(() => expect(screen.getByText('Login page')).toBeInTheDocument())
  })
})
