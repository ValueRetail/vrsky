/**
 * App mounts the new-version banner (#551) outside the routes, so it shows on
 * every page, the builder included (which isn't under RootLayout).
 *
 * The banner, the layout and every page are stubbed, so this test doesn't
 * load the pages (and doesn't pull them into the coverage report); auth is a
 * signed-in user.
 */

import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'

vi.mock('@/components/Common/NewVersionBanner', () => ({ default: () => <div data-testid="new-version-banner" /> }))
vi.mock('@/components/Layout/RootLayout', async () => {
  const { Outlet } = await import('react-router-dom')
  return { default: () => <div data-testid="root-layout"><Outlet /></div> }
})
vi.mock('@/pages/PipelineBuilderPage', () => ({ default: () => <div data-testid="builder" /> }))
vi.mock('@/pages/LoginPage', () => ({ default: () => <div data-testid="login" /> }))
vi.mock('@/pages/ConnectionsList', () => ({ default: () => <div data-testid="connections" /> }))
vi.mock('@/pages/ConnectionDetail', () => ({ default: () => null }))
vi.mock('@/pages/TestDataPage', () => ({ default: () => null }))
vi.mock('@/pages/RegisterPage', () => ({ default: () => null }))
vi.mock('@/pages/ForgotPasswordPage', () => ({ default: () => null }))
vi.mock('@/pages/ResetPasswordPage', () => ({ default: () => null }))
vi.mock('@/pages/OAuthConnected', () => ({ default: () => null }))
vi.mock('@/pages/NotFound', () => ({ default: () => null }))
vi.mock('@/pages/ConnectionRequestsPage', () => ({ default: () => null }))
vi.mock('@/pages/TenantConnectionsPage', () => ({ default: () => null }))
vi.mock('@/pages/ApiKeyPage', () => ({ default: () => null }))
vi.mock('@/pages/AuditPage', () => ({ default: () => null }))
vi.mock('@/pages/UsersPage', () => ({ default: () => null }))
vi.mock('@/pages/AgentsPage', () => ({ default: () => null }))
vi.mock('@/pages/InviteAcceptPage', () => ({ default: () => null }))
vi.mock('@/pages/UsagePage', () => ({ default: () => null }))
vi.mock('@/pages/OAuthProvidersPage', () => ({ default: () => null }))
vi.mock('@/pages/NotificationsPage', () => ({ default: () => null }))
vi.mock('@/pages/PlanPage', () => ({ default: () => null }))
vi.mock('@/pages/PlatformTenantsPage', () => ({ default: () => null }))
vi.mock('@/onboarding/OnboardingWizard', () => ({ default: () => null }))
vi.mock('@/store/authStore', () => {
  const state = {
    isAuthenticated: true,
    isInitialized: true,
    isLoading: false,
    user: null,
    currentTenant: null,
    checkAuth: async () => {},
  }
  const useAuthStore = (sel?: (s: typeof state) => unknown) => (sel ? sel(state) : state)
  useAuthStore.getState = () => state
  return { useAuthStore }
})

import App from './App'

afterEach(() => {
  window.history.pushState({}, '', '/')
})

describe('App', () => {
  it('renders the new-version banner on the builder route, outside RootLayout', () => {
    window.history.pushState({}, '', '/connections/create')
    render(<App />)
    expect(screen.getByTestId('builder')).toBeInTheDocument()
    expect(screen.queryByTestId('root-layout')).not.toBeInTheDocument()
    expect(screen.getByTestId('new-version-banner')).toBeInTheDocument()
  })

  it('renders the new-version banner on a route under RootLayout', () => {
    window.history.pushState({}, '', '/connections')
    render(<App />)
    expect(screen.getByTestId('connections')).toBeInTheDocument()
    expect(screen.getByTestId('new-version-banner')).toBeInTheDocument()
  })

  it('renders the new-version banner on a public route', () => {
    window.history.pushState({}, '', '/login')
    render(<App />)
    expect(screen.getByTestId('login')).toBeInTheDocument()
    expect(screen.getByTestId('new-version-banner')).toBeInTheDocument()
  })
})
