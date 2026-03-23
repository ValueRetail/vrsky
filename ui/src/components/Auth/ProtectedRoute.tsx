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
  const { isAuthenticated, isInitialized, isLoading, checkAuth } = useAuthStore()

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

  // Redirect to login if not authenticated
  if (!isAuthenticated) {
    return <Navigate to="/login" state={{ from: location.pathname }} replace />
  }

  // Render children if authenticated
  return <>{children}</>
}
