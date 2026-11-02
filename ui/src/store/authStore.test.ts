/**
 * An expired session drops the user (which is what sends them to the login
 * page) and remembers why; logging in again clears that.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'

const login = vi.fn()
const getMe = vi.fn()
const clearSessionToken = vi.fn()
const logout = vi.fn()
let hasToken = true
vi.mock('@/services/authService', () => ({
  login: (...a: unknown[]) => login(...a),
  getMe: () => getMe(),
  logout: () => logout(),
  register: vi.fn(),
  hasSessionToken: () => hasToken,
  getSessionToken: () => 'tok',
  clearSessionToken: () => clearSessionToken(),
}))

import { useAuthStore } from './authStore'
import { reportUnauthorized } from '@/services/sessionExpiry'
import { useUIStore } from '@/store/uiStore'

const user = { id: 'u1', email: 'a@b.c' }
const tenant = { id: 't1', name: 'WS', status: 'active' }

beforeEach(() => {
  login.mockReset()
  getMe.mockReset()
  clearSessionToken.mockReset()
  logout.mockReset().mockResolvedValue(undefined)
  hasToken = true
  localStorage.clear()
  useAuthStore.setState({
    user: user as never, tenants: [tenant] as never, currentTenant: tenant as never,
    isAuthenticated: true, isInitialized: true, isLoading: false, error: null, sessionExpired: false,
  })
})

describe('authStore session expiry', () => {
  it('expireSession drops the user and the token and says why', () => {
    localStorage.setItem('vrsky:selectedTenantId', 't1')
    useAuthStore.getState().expireSession()
    const s = useAuthStore.getState()
    expect(s.isAuthenticated).toBe(false)
    expect(s.user).toBeNull()
    expect(s.currentTenant).toBeNull()
    expect(s.sessionExpired).toBe(true)
    expect(clearSessionToken).toHaveBeenCalled()
    // The workspace choice survives, so the next login returns to it.
    expect(localStorage.getItem('vrsky:selectedTenantId')).toBe('t1')
  })

  it('is what a confirmed 401 triggers', async () => {
    // The store registered itself with services/sessionExpiry on load.
    // What getMe does on its own 401: drop the token, return null.
    getMe.mockImplementation(async () => { hasToken = false; return null })
    // The request that got the 401 has already raised its own error toast.
    useUIStore.getState().addNotification({ type: 'error', title: 'Error', message: 'Failed to rotate API key: invalid or expired session' })
    expect(useUIStore.getState().notifications).toHaveLength(1)
    await reportUnauthorized()
    expect(useAuthStore.getState().sessionExpired).toBe(true)
    expect(useAuthStore.getState().isAuthenticated).toBe(false)
    // …and the login page explains it better than that toast did.
    expect(useUIStore.getState().notifications).toHaveLength(0)
  })

  it('a successful login clears the notice', async () => {
    useAuthStore.setState({ sessionExpired: true, isAuthenticated: false, user: null })
    login.mockResolvedValue({ success: true, user })
    getMe.mockResolvedValue({ user, tenants: [tenant] })
    expect(await useAuthStore.getState().login('a@b.c', 'pw')).toBe(true)
    expect(useAuthStore.getState().sessionExpired).toBe(false)
    expect(useAuthStore.getState().isAuthenticated).toBe(true)
  })
})

describe('authStore session lifecycle', () => {
  const other = { id: 't2', name: 'Other', status: 'active' }

  it('checkAuth restores the user and the workspace they had selected', async () => {
    localStorage.setItem('vrsky:selectedTenantId', 't2')
    getMe.mockResolvedValue({ user, tenants: [tenant, other], current_tenant: tenant })
    useAuthStore.setState({ user: null, tenants: [], currentTenant: null, isAuthenticated: false, isInitialized: false })
    await useAuthStore.getState().checkAuth()
    const s = useAuthStore.getState()
    expect(s.isAuthenticated).toBe(true)
    expect(s.isInitialized).toBe(true)
    expect(s.currentTenant?.id).toBe('t2')
  })

  it('checkAuth without a token, or with one the server rejects, leaves the user signed out', async () => {
    hasToken = false
    await useAuthStore.getState().checkAuth()
    expect(useAuthStore.getState().isAuthenticated).toBe(false)
    expect(getMe).not.toHaveBeenCalled()

    // A 401 on /me: getMe drops the token and returns null.
    hasToken = true
    getMe.mockImplementation(async () => { hasToken = false; return null })
    useAuthStore.setState({ isAuthenticated: true, user: user as never })
    await useAuthStore.getState().checkAuth()
    expect(useAuthStore.getState().isAuthenticated).toBe(false)
    expect(useAuthStore.getState().user).toBeNull()
    expect(useAuthStore.getState().tenants).toEqual([])
    expect(useAuthStore.getState().loadFailed).toBe(false)
  })

  describe('checkAuth when /me fails without a 401 (#611)', () => {
    // getMe returns null for a network error or a 5xx too, but keeps the token.
    beforeEach(() => {
      useUIStore.getState().clearNotifications()
      getMe.mockImplementation(async () => null)
    })

    it('keeps the user and workspaces and says the refresh failed', async () => {
      await useAuthStore.getState().checkAuth()
      const s = useAuthStore.getState()
      expect(s.isAuthenticated).toBe(true)
      expect(s.user).toEqual(user)
      expect(s.tenants).toEqual([tenant])
      expect(s.currentTenant).toEqual(tenant)
      expect(s.isLoading).toBe(false)
      expect(s.sessionExpired).toBe(false)
      expect(useUIStore.getState().notifications.map((n) => n.title)).toEqual(['Could not refresh your account'])
    })

    it('at app load with nothing cached stays signed out but flags loadFailed', async () => {
      useAuthStore.setState({ user: null, tenants: [], currentTenant: null, isAuthenticated: false, isInitialized: false })
      await useAuthStore.getState().checkAuth()
      const s = useAuthStore.getState()
      expect(s.isInitialized).toBe(true)
      expect(s.isLoading).toBe(false)
      expect(s.isAuthenticated).toBe(false)
      expect(s.loadFailed).toBe(true)
      expect(clearSessionToken).not.toHaveBeenCalled()
    })

    it('treats a throw from getMe the same way', async () => {
      getMe.mockRejectedValue(new Error('boom'))
      await useAuthStore.getState().checkAuth()
      expect(useAuthStore.getState().isAuthenticated).toBe(true)
      expect(useAuthStore.getState().tenants).toEqual([tenant])
    })

    it('a retry that works clears loadFailed', async () => {
      useAuthStore.setState({ user: null, tenants: [], currentTenant: null, isAuthenticated: false, loadFailed: true })
      getMe.mockResolvedValue({ user, tenants: [tenant], current_tenant: tenant })
      await useAuthStore.getState().checkAuth()
      expect(useAuthStore.getState().isAuthenticated).toBe(true)
      expect(useAuthStore.getState().loadFailed).toBe(false)
    })

    it('a successful login clears loadFailed', async () => {
      useAuthStore.setState({ user: null, tenants: [], currentTenant: null, isAuthenticated: false, loadFailed: true })
      login.mockResolvedValue({ success: true, user })
      // /me still fails after the login, so only login itself can clear the flag.
      getMe.mockImplementation(async () => null)
      expect(await useAuthStore.getState().login('a@b.c', 'pw')).toBe(true)
      expect(useAuthStore.getState().loadFailed).toBe(false)
      expect(useAuthStore.getState().isAuthenticated).toBe(true)
    })

    it('logout clears loadFailed', async () => {
      useAuthStore.setState({ loadFailed: true })
      await useAuthStore.getState().logout()
      expect(useAuthStore.getState().loadFailed).toBe(false)
    })

    it('a 401 on the retry clears loadFailed and signs out', async () => {
      useAuthStore.setState({ user: null, tenants: [], currentTenant: null, isAuthenticated: false, loadFailed: true })
      getMe.mockImplementation(async () => { hasToken = false; return null })
      await useAuthStore.getState().checkAuth()
      expect(useAuthStore.getState().loadFailed).toBe(false)
      expect(useAuthStore.getState().isAuthenticated).toBe(false)
    })
  })

  it('logout signs out, forgets the workspace choice and the expiry notice', async () => {
    localStorage.setItem('vrsky:selectedTenantId', 't1')
    useAuthStore.setState({ sessionExpired: true })
    await useAuthStore.getState().logout()
    const s = useAuthStore.getState()
    expect(logout).toHaveBeenCalled()
    expect(s.isAuthenticated).toBe(false)
    expect(s.sessionExpired).toBe(false)
    expect(localStorage.getItem('vrsky:selectedTenantId')).toBeNull()
  })

  it('a failed login reports the reason and stays signed out', async () => {
    useAuthStore.setState({ isAuthenticated: false, user: null })
    login.mockResolvedValue({ success: false, message: 'Invalid email or password' })
    expect(await useAuthStore.getState().login('a@b.c', 'bad')).toBe(false)
    expect(useAuthStore.getState().error).toBe('Invalid email or password')
    login.mockRejectedValue(new Error('network down'))
    expect(await useAuthStore.getState().login('a@b.c', 'pw')).toBe(false)
    expect(useAuthStore.getState().error).toBe('network down')
  })

  it('switchTenant only switches to an active workspace the user has', () => {
    useAuthStore.setState({ tenants: [tenant, other] as never, currentTenant: tenant as never, error: null })
    useAuthStore.getState().switchTenant(other as never)
    expect(useAuthStore.getState().currentTenant?.id).toBe('t2')
    expect(localStorage.getItem('vrsky:selectedTenantId')).toBe('t2')
    useAuthStore.getState().switchTenant({ id: 'nope', name: 'x', status: 'active' } as never)
    expect(useAuthStore.getState().currentTenant?.id).toBe('t2')
    expect(useAuthStore.getState().error).toMatch(/Unable to switch tenant/)
    useAuthStore.getState().clearError()
    expect(useAuthStore.getState().error).toBeNull()
  })
})
