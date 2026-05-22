/**
 * Login Page
 * Email/password authentication form
 */

import { useState, useEffect } from 'react'
import { Link, useNavigate, useLocation } from 'react-router-dom'
import axios from 'axios'
import { useAuthStore } from '@/store/authStore'
import { config } from '@/config/env'

export default function LoginPage() {
  const navigate = useNavigate()
  const location = useLocation()
  const { login, isAuthenticated, isLoading, error, clearError } = useAuthStore()

  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [localError, setLocalError] = useState<string | null>(null)

  // Single sign-on (Phase 1C / #68).
  // The user enters their workspace slug; we ask the backend if SSO is
  // configured for it and, if so, surface a "Sign in with …" button.
  const [ssoWorkspace, setSsoWorkspace] = useState('')
  const [ssoAvailable, setSsoAvailable] = useState<{ available: boolean; label?: string } | null>(null)
  const [ssoChecking, setSsoChecking] = useState(false)

  const checkSSO = async () => {
    if (!ssoWorkspace.trim()) return
    setSsoChecking(true)
    setSsoAvailable(null)
    try {
      const resp = await axios.get<{ available: boolean; label?: string }>(
        `${config.apiUrl}/api/v1/auth/oidc/${encodeURIComponent(ssoWorkspace.trim())}/available`,
      )
      setSsoAvailable(resp.data)
      if (!resp.data.available) {
        setLocalError('No SSO is configured for that workspace.')
      }
    } catch {
      setLocalError('Could not check SSO for that workspace.')
    } finally {
      setSsoChecking(false)
    }
  }

  const startSSO = () => {
    if (!ssoWorkspace.trim()) return
    // Full page navigation; the backend redirects through the IdP and back
    // to our /api/v1/auth/oidc/callback, which then lands at "/" with the
    // vrsky_session cookie set.
    window.location.href = `${config.apiUrl}/api/v1/auth/oidc/${encodeURIComponent(ssoWorkspace.trim())}/login`
  }

  // Redirect if already authenticated
  useEffect(() => {
    if (isAuthenticated) {
      const from = (location.state as { from?: string })?.from || '/'
      navigate(from, { replace: true })
    }
  }, [isAuthenticated, navigate, location])

  // Clear errors when component mounts
  useEffect(() => {
    clearError()
  }, [clearError])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setLocalError(null)

    // Basic validation
    if (!email || !password) {
      setLocalError('Email and password are required')
      return
    }

    const success = await login(email, password)
    if (success) {
      const from = (location.state as { from?: string })?.from || '/'
      navigate(from, { replace: true })
    }
  }

  const displayError = localError || error

  return (
    <div className="min-h-screen bg-neutral-50 dark:bg-neutral-950 flex items-center justify-center py-12 px-4 sm:px-6 lg:px-8">
      <div className="max-w-md w-full space-y-8">
        {/* Header */}
        <div className="text-center">
          <h1 className="text-4xl font-bold bg-gradient-to-r from-primary-600 to-secondary-600 dark:from-primary-400 dark:to-secondary-400 bg-clip-text text-transparent">
            VRSky
          </h1>
          <h2 className="mt-6 text-2xl font-semibold text-neutral-900 dark:text-neutral-100">
            Sign in to your account
          </h2>
          <p className="mt-2 text-sm text-neutral-600 dark:text-neutral-400">
            Or{' '}
            <Link
              to="/register"
              className="font-medium text-primary-600 hover:text-primary-500 dark:text-primary-400 dark:hover:text-primary-300"
            >
              create a new account
            </Link>
          </p>
        </div>

        {/* Login Form */}
        <form className="mt-8 space-y-6" onSubmit={handleSubmit}>
          {/* Error Message */}
          {displayError && (
            <div className="rounded-md bg-red-50 dark:bg-red-900/20 p-4 border border-red-200 dark:border-red-800">
              <p className="text-sm text-red-700 dark:text-red-400">
                <span className="font-semibold">Error:</span> {displayError}
              </p>
            </div>
          )}

          <div className="space-y-4">
            {/* Email Input */}
            <div>
              <label htmlFor="email" className="block text-sm font-medium text-neutral-700 dark:text-neutral-300">
                Email address
              </label>
              <input
                id="email"
                name="email"
                type="email"
                autoComplete="email"
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className="mt-1 block w-full px-3 py-2 border border-neutral-300 dark:border-neutral-600 rounded-md shadow-sm placeholder-neutral-400 dark:placeholder-neutral-500 bg-white dark:bg-neutral-800 text-neutral-900 dark:text-neutral-100 focus:outline-none focus:ring-primary-500 focus:border-primary-500"
                placeholder="you@example.com"
              />
            </div>

            {/* Password Input */}
            <div>
              <label htmlFor="password" className="block text-sm font-medium text-neutral-700 dark:text-neutral-300">
                Password
              </label>
              <input
                id="password"
                name="password"
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="mt-1 block w-full px-3 py-2 border border-neutral-300 dark:border-neutral-600 rounded-md shadow-sm placeholder-neutral-400 dark:placeholder-neutral-500 bg-white dark:bg-neutral-800 text-neutral-900 dark:text-neutral-100 focus:outline-none focus:ring-primary-500 focus:border-primary-500"
                placeholder="Enter your password"
              />
            </div>
          </div>

          {/* Forgot Password Link */}
          <div className="flex items-center justify-end">
            <Link
              to="/forgot-password"
              className="text-sm font-medium text-primary-600 hover:text-primary-500 dark:text-primary-400 dark:hover:text-primary-300"
            >
              Forgot your password?
            </Link>
          </div>

          {/* Submit Button */}
          <button
            type="submit"
            disabled={isLoading}
            className="w-full flex justify-center py-2 px-4 border border-transparent rounded-md shadow-sm text-sm font-medium text-white bg-primary-600 hover:bg-primary-700 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-primary-500 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          >
            {isLoading ? 'Signing in...' : 'Sign in'}
          </button>
        </form>

        {/* Single sign-on — Phase 1C (#68) */}
        <div className="mt-8 pt-6 border-t border-neutral-200 dark:border-neutral-700">
          <div className="text-center text-xs uppercase tracking-wide text-neutral-500 dark:text-neutral-500 mb-3">
            Or sign in via SSO
          </div>
          <div className="flex gap-2">
            <input
              type="text"
              placeholder="Workspace slug"
              value={ssoWorkspace}
              onChange={(e) => setSsoWorkspace(e.target.value)}
              className="flex-1 px-3 py-2 border border-neutral-300 dark:border-neutral-600 rounded-md text-sm bg-white dark:bg-neutral-800 text-neutral-900 dark:text-neutral-100"
            />
            <button
              type="button"
              onClick={checkSSO}
              disabled={ssoChecking || !ssoWorkspace.trim()}
              className="px-4 py-2 text-sm border border-neutral-300 dark:border-neutral-600 rounded-md bg-white dark:bg-neutral-800 text-neutral-700 dark:text-neutral-200 hover:bg-neutral-50 dark:hover:bg-neutral-700 disabled:opacity-50"
            >
              {ssoChecking ? 'Checking…' : 'Check'}
            </button>
          </div>
          {ssoAvailable?.available && (
            <button
              type="button"
              onClick={startSSO}
              className="mt-3 w-full py-2 px-4 rounded-md text-sm font-medium text-white bg-emerald-600 hover:bg-emerald-700"
            >
              Sign in with {ssoAvailable.label || 'SSO'}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
