/**
 * Auth Store
 * Manages authentication state with Zustand
 */

import { create } from 'zustand'
import type { User } from '@/types/models'
import * as authService from '@/services/authService'

interface AuthState {
  // State
  user: User | null
  isAuthenticated: boolean
  isLoading: boolean
  isInitialized: boolean
  error: string | null

  // Actions
  login: (email: string, password: string) => Promise<boolean>
  register: (email: string, password: string, fullName: string) => Promise<{ success: boolean; message?: string }>
  logout: () => Promise<void>
  checkAuth: () => Promise<void>
  clearError: () => void
}

export const useAuthStore = create<AuthState>((set, get) => ({
  // Initial state
  user: null,
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
  register: async (email: string, password: string, fullName: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await authService.register({
        email,
        password,
        full_name: fullName,
      })
      
      set({ isLoading: false })
      
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
      set({
        user: null,
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
    // Skip if already initialized or no token
    if (get().isInitialized) {
      return
    }

    // If no session token, mark as initialized but not authenticated
    if (!authService.hasSessionToken()) {
      set({
        isInitialized: true,
        isAuthenticated: false,
        user: null,
      })
      return
    }

    set({ isLoading: true })

    try {
      const user = await authService.getMe()
      
      if (user) {
        set({
          user,
          isAuthenticated: true,
          isInitialized: true,
          isLoading: false,
        })
      } else {
        set({
          user: null,
          isAuthenticated: false,
          isInitialized: true,
          isLoading: false,
        })
      }
    } catch (error) {
      set({
        user: null,
        isAuthenticated: false,
        isInitialized: true,
        isLoading: false,
      })
    }
  },

  /**
   * Clear error message
   */
  clearError: () => set({ error: null }),
}))
