/**
 * Auth Store
 * Manages authentication state with Zustand
 */

import { create } from 'zustand'
import type { User, Tenant } from '@/types/models'
import * as authService from '@/services/authService'
import { setActiveTenantId } from '@/services/api'
import { setSessionExpiredHandler } from '@/services/sessionExpiry'
import { useUIStore } from '@/store/uiStore'

const SELECTED_TENANT_KEY = 'vrsky:selectedTenantId'

interface AuthState {
  // State
  user: User | null
  isPlatformOperator: boolean
  tenants: Tenant[]
  currentTenant: Tenant | null
  isAuthenticated: boolean
  isLoading: boolean
  isInitialized: boolean
  error: string | null
  /** The session ran out while the app was open; the login page says so. */
  sessionExpired: boolean
  /**
   * /me could not be answered (network error, 5xx) while there was no user
   * to keep. The session is probably fine, so ProtectedRoute offers a retry
   * instead of the login page.
   */
  loadFailed: boolean

  // Actions
  expireSession: () => void
  login: (email: string, password: string) => Promise<boolean>
  register: (email: string, password: string, fullName: string, workspaceName: string) => Promise<{ success: boolean; message?: string }>
  logout: () => Promise<void>
  checkAuth: (options?: { background?: boolean }) => Promise<void>
  /**
   * Re-read /me for a signed-in user without raising isLoading, so
   * ProtectedRoute keeps the page mounted. Same sign-out rules as checkAuth.
   * Without a user to keep it is a plain checkAuth.
   */
  refreshAuth: () => Promise<void>
  switchTenant: (tenant: Tenant) => void
  clearError: () => void
}

export const useAuthStore = create<AuthState>((set, get) => ({
  // Initial state
  user: null,
  isPlatformOperator: false,
  tenants: [],
  currentTenant: null,
  isAuthenticated: false,
  isLoading: false,
  isInitialized: false,
  error: null,
  sessionExpired: false,
  loadFailed: false,

  /**
   * The server no longer accepts the session (see services/sessionExpiry).
   * Dropping the user is all it takes: ProtectedRoute sends an
   * unauthenticated visitor to the login page and remembers where they
   * were. The selected workspace is kept so they come back to the same one.
   */
  expireSession: (): void => {
    authService.clearSessionToken()
    set({
      user: null,
      isPlatformOperator: false,
      tenants: [],
      currentTenant: null,
      isAuthenticated: false,
      isInitialized: true,
      isLoading: false,
      error: null,
      sessionExpired: true,
      loadFailed: false,
    })
  },

  /**
   * Login with email and password
   */
  login: async (email: string, password: string): Promise<boolean> => {
    set({ isLoading: true, error: null })

    try {
      const response = await authService.login({ email, password })

      if (response.success && response.user) {
        set({
          user: response.user,
          isAuthenticated: true,
          isLoading: false,
          error: null,
          sessionExpired: false,
          loadFailed: false,
        })
        // Fetch tenants after login
        await get().checkAuth()
        return true
      } else {
        set({
          isLoading: false,
          error: response.message || 'Login failed',
        })
        return false
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Login failed'
      set({
        isLoading: false,
        error: message,
      })
      return false
    }
  },

  /**
   * Register a new user
   */
  register: async (email: string, password: string, fullName: string, workspaceName: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await authService.register({
        email,
        password,
        full_name: fullName,
        workspace_name: workspaceName,
      })

      set({ isLoading: false, error: response.success ? null : (response.message || 'Registration failed') })

      return {
        success: response.success,
        message: response.message,
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Registration failed'
      set({
        isLoading: false,
        error: message,
      })
      return {
        success: false,
        message,
      }
    }
  },

  /**
   * Logout the current user
   */
  logout: async (): Promise<void> => {
    set({ isLoading: true })

    try {
      await authService.logout()
    } finally {
      localStorage.removeItem(SELECTED_TENANT_KEY)
      set({
        user: null,
        isPlatformOperator: false,
        tenants: [],
        currentTenant: null,
        isAuthenticated: false,
        isLoading: false,
        error: null,
        sessionExpired: false,
        loadFailed: false,
      })
    }
  },

  /**
   * Check if user is authenticated (on app load).
   *
   * background: skip isLoading, which ProtectedRoute answers with the spinner
   * instead of the page (and the page remounts: open panels, selection and
   * history are lost). Everything else is the same: a 401/403 (getMe dropped
   * the token) signs out, a transient failure keeps the user.
   */
  checkAuth: async (options?: { background?: boolean }): Promise<void> => {
    const background = options?.background === true
    // Spread into every set() that would touch isLoading.
    const loading = (value: boolean) => (background ? {} : { isLoading: value })

    // If no session token, mark as initialized but not authenticated
    if (!authService.hasSessionToken()) {
      set({
        isInitialized: true,
        isAuthenticated: false,
        user: null,
        isPlatformOperator: false,
        tenants: [],
        currentTenant: null,
        loadFailed: false,
      })
      return
    }

    set(loading(true))

    let meData: Awaited<ReturnType<typeof authService.getMe>> = null
    try {
      meData = await authService.getMe()
    } catch {
      // getMe does not throw today; treat a surprise like any failed answer.
    }

    if (meData && meData.user) {
      const tenants = meData.tenants || []
      // Restore previously selected tenant if still valid
      const savedTenantId = localStorage.getItem(SELECTED_TENANT_KEY)
      const savedTenant = savedTenantId
        ? tenants.find((t) => t.id === savedTenantId && t.status === 'active')
        : null
      // Otherwise the server's pick, if it is active, then the first active
      // workspace. /me's current_tenant is just tenants[0] (oldest first) and
      // can be provisioning or failed, which switchTenant refuses. With no
      // active workspace at all the user has none selected, the state a user
      // with no workspaces is already in.
      const serverTenant = meData.current_tenant
      const currentTenant =
        savedTenant ||
        (serverTenant && serverTenant.status === 'active' ? serverTenant : null) ||
        tenants.find((t) => t.status === 'active') ||
        null

      // Update the API client's tenant ID
      if (currentTenant) {
        setActiveTenantId(currentTenant.id)
        localStorage.setItem(SELECTED_TENANT_KEY, currentTenant.id)
      } else {
        // No active workspace: do not keep sending the build-time default or
        // the previous workspace's id. Requests go without the header and the
        // server answers 400.
        setActiveTenantId('')
      }

      set({
        user: meData.user,
        isPlatformOperator: meData.is_platform_operator === true,
        tenants,
        currentTenant,
        isAuthenticated: true,
        isInitialized: true,
        ...loading(false),
        loadFailed: false,
      })
      return
    }

    // getMe drops the token only when the server rejects the session with a 401
    // or 403, the way reportUnauthorized in services/sessionExpiry tells them apart.
    // A network error or a 5xx leaves it, and that is no reason to sign
    // out a user whose session still stands.
    if (authService.hasSessionToken()) {
      if (get().user) {
        set({ isInitialized: true, ...loading(false) })
        useUIStore.getState().showErrorNotification(
          'Could not refresh your account',
          'The server did not answer. You are still signed in; workspaces and settings may be out of date.',
        )
      } else {
        set({ isAuthenticated: false, isInitialized: true, ...loading(false), loadFailed: true })
      }
      return
    }

    set({
      user: null,
      isPlatformOperator: false,
      tenants: [],
      currentTenant: null,
      isAuthenticated: false,
      isInitialized: true,
      ...loading(false),
      loadFailed: false,
    })
  },

  refreshAuth: (): Promise<void> => get().checkAuth({ background: get().user !== null }),

  /**
   * Switch to a different tenant
   */
  switchTenant: (tenant: Tenant) => {
    const { tenants } = get()
    const validTenant = tenants.find(
      (t) => t.id === tenant.id && t.status === 'active',
    )

    if (!validTenant) {
      set({
        error: 'Unable to switch tenant. The selected tenant is not active or not assigned to this user.',
      })
      return
    }

    setActiveTenantId(validTenant.id)
    localStorage.setItem(SELECTED_TENANT_KEY, validTenant.id)
    set({ currentTenant: validTenant })
  },

  /**
   * Clear error message
   */
  clearError: () => set({ error: null }),
}))

// Any 401 that turns out to be an expired session ends up here. The request
// that hit the 401 has usually raised its own error toast by now ("Failed to
// rotate API key…"); with the user on their way to the login page, which
// says what happened, those toasts are noise.
setSessionExpiredHandler(() => {
  useAuthStore.getState().expireSession()
  useUIStore.getState().clearNotifications()
})
