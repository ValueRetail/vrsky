/**
 * Forgot password (plans/auth-leftovers.md): one email field. The API's
 * answer is the same whether or not the address has an account; an
 * installation that cannot send mail says so (503), and the limiter's 429
 * is shown as it comes.
 */

import { useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { forgotPassword } from '@/services/authService'
import { apiErrorMessage } from '@/utils/apiError'

export default function ForgotPasswordPage() {
  const [email, setEmail] = useState('')
  const [busy, setBusy] = useState(false)
  const [sent, setSent] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const res = await forgotPassword({ email: email.trim() })
      setSent(res.message || 'If that address has an account, a password reset link is on its way.')
    } catch (err) {
      setError(apiErrorMessage(err, 'Could not request a reset. Try again in a moment.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="min-h-screen bg-neutral-50 dark:bg-neutral-950 flex items-center justify-center py-12 px-4 sm:px-6 lg:px-8">
      <div className="max-w-md w-full space-y-8">
        <div className="text-center">
          <h2 className="text-2xl font-semibold text-neutral-900 dark:text-neutral-100">Forgot your password?</h2>
          <p className="mt-2 text-sm text-neutral-600 dark:text-neutral-400">Enter the email you sign in with and we'll send you a link to set a new one.</p>
        </div>

        {sent ? (
          <div role="status" className="rounded-md bg-emerald-50 dark:bg-emerald-900/20 p-4 border border-emerald-200 dark:border-emerald-800">
            <p className="text-sm text-emerald-800 dark:text-emerald-300">{sent}</p>
          </div>
        ) : (
          <form className="mt-8 space-y-6" onSubmit={submit}>
            {error && (
              <div role="alert" className="rounded-md bg-red-50 dark:bg-red-900/20 p-4 border border-red-200 dark:border-red-800">
                <p className="text-sm text-red-700 dark:text-red-400">{error}</p>
              </div>
            )}
            <div>
              <label htmlFor="email" className="block text-sm font-medium text-neutral-700 dark:text-neutral-300">
                Email address
              </label>
              <input
                id="email"
                type="email"
                autoComplete="email"
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className="mt-1 block w-full px-3 py-2 border border-neutral-300 dark:border-neutral-600 rounded-md shadow-sm placeholder-neutral-400 dark:placeholder-neutral-500 bg-white dark:bg-neutral-800 text-neutral-900 dark:text-neutral-100 focus:outline-none focus:ring-primary-500 focus:border-primary-500 sm:text-sm"
                placeholder="you@example.com"
              />
            </div>
            <button type="submit" disabled={busy || !email} className="w-full flex justify-center py-2 px-4 border border-transparent rounded-md shadow-sm text-sm font-medium text-white bg-primary-600 hover:bg-primary-700 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-primary-500 disabled:opacity-50">
              {busy ? 'Sending…' : 'Send reset link'}
            </button>
          </form>
        )}
        <p className="text-center text-sm text-neutral-600 dark:text-neutral-400">
          <Link to="/login" className="font-medium text-primary-600 hover:text-primary-500 dark:text-primary-400 dark:hover:text-primary-300">Back to sign in</Link>
        </p>
      </div>
    </div>
  )
}
