/**
 * Protected Route Component
 * Redirects unauthenticated users to login page
 */

import { useEffect } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { useAuthStore } from '@/store/authStore'

interface ProtectedRouteProps {
  children: React.ReactNode
}

export default function ProtectedRoute({ children }: ProtectedRouteProps) {
  const location = useLocation()
  const { isAuthenticated, isInitialized, isLoading, loadFailed, checkAuth, logout } = useAuthStore()

  // Check authentication on mount
  useEffect(() => {
    checkAuth()
  }, [checkAuth])

  // Show loading state while checking auth
  if (!isInitialized || isLoading) {
    return (
      <div className="min-h-screen bg-neutral-50 dark:bg-neutral-950 flex items-center justify-center">
        <div className="flex flex-col items-center gap-4">
          <div className="w-8 h-8 border-4 border-primary-200 dark:border-primary-800 border-t-primary-600 dark:border-t-primary-400 rounded-full animate-spin"></div>
          <p className="text-sm text-neutral-600 dark:text-neutral-400">
            Loading...
          </p>
        </div>
      </div>
    )
  }

  // /me could not be answered and nothing is cached: the session is probably
  // fine, so offer a retry instead of the login page.
  if (!isAuthenticated && loadFailed) {
    return (
      <div className="min-h-screen bg-neutral-50 dark:bg-neutral-950 flex items-center justify-center">
        <div className="flex flex-col items-center gap-4 text-center px-4">
          <p className="text-sm text-neutral-700 dark:text-neutral-300">
            Could not reach the server. Your session is still there.
          </p>
          <button
            type="button"
            onClick={() => { void checkAuth() }}
            className="px-4 py-2 text-sm font-medium rounded-md bg-primary-600 text-white hover:bg-primary-700"
          >
            Retry
          </button>
          <button
            type="button"
            onClick={() => { logout().catch(() => { /* the local session is cleared either way */ }) }}
            className="text-sm text-neutral-600 dark:text-neutral-400 underline hover:text-neutral-900 dark:hover:text-neutral-100"
          >
            Sign in again
          </button>
        </div>
      </div>
    )
  }

  // Redirect to login if not authenticated
  if (!isAuthenticated) {
    return <Navigate to="/login" state={{ from: location.pathname }} replace />
  }

  // Render children if authenticated
  return <>{children}</>
}
