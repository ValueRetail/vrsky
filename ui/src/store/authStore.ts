/**
 * Auth Store
 * Manages authentication state with Zustand
 */

import { create } from 'zustand'
import type { User, Tenant } from '@/types/models'
import * as authService from '@/services/authService'
import { setActiveTenantId } from '@/services/api'

const SELECTED_TENANT_KEY = 'vrsky:selectedTenantId'

interface AuthState {
  // State
  user: User | null
  tenants: Tenant[]
  currentTenant: Tenant | null
  isAuthenticated: boolean
  isLoading: boolean
  isInitialized: boolean
  error: string | null

  // Actions
  login: (email: string, password: string) => Promise<boolean>
  register: (email: string, password: string, fullName: string, workspaceName: string) => Promise<{ success: boolean; message?: string }>
  logout: () => Promise<void>
  checkAuth: () => Promise<void>
  switchTenant: (tenant: Tenant) => void
  clearError: () => void
}

export const useAuthStore = create<AuthState>((set, get) => ({
  // Initial state
  user: null,
  tenants: [],
  currentTenant: null,
  isAuthenticated: false,
  isLoading: false,
  isInitialized: false,
  error: null,

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
        tenants: [],
        currentTenant: null,
        isAuthenticated: false,
        isLoading: false,
        error: null,
      })
    }
  },

  /**
   * Check if user is authenticated (on app load)
   */
  checkAuth: async (): Promise<void> => {
    // If no session token, mark as initialized but not authenticated
    if (!authService.hasSessionToken()) {
      set({
        isInitialized: true,
        isAuthenticated: false,
        user: null,
        tenants: [],
        currentTenant: null,
      })
      return
    }

    set({ isLoading: true })

    try {
      const meData = await authService.getMe()

      if (meData && meData.user) {
        const tenants = meData.tenants || []
        // Restore previously selected tenant if still valid
        const savedTenantId = localStorage.getItem(SELECTED_TENANT_KEY)
        const savedTenant = savedTenantId
          ? tenants.find((t) => t.id === savedTenantId && (t as any).status === 'active')
          : null
        const currentTenant = savedTenant || meData.current_tenant || (tenants.length > 0 ? tenants[0] : null)

        // Update the API client's tenant ID
        if (currentTenant) {
          setActiveTenantId(currentTenant.id)
          localStorage.setItem(SELECTED_TENANT_KEY, currentTenant.id)
        }

        set({
          user: meData.user,
          tenants,
          currentTenant,
          isAuthenticated: true,
          isInitialized: true,
          isLoading: false,
        })
      } else {
        set({
          user: null,
          tenants: [],
          currentTenant: null,
          isAuthenticated: false,
          isInitialized: true,
          isLoading: false,
        })
      }
    } catch {
      set({
        user: null,
        tenants: [],
        currentTenant: null,
        isAuthenticated: false,
        isInitialized: true,
        isLoading: false,
      })
    }
  },

  /**
   * Switch to a different tenant
   */
  switchTenant: (tenant: Tenant) => {
    const { tenants } = get()
    const validTenant = tenants.find(
      (t) => t.id === tenant.id && (t as any).status === 'active',
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
