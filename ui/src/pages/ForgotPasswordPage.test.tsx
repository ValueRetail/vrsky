/**
 * The forgot-password page shows the API's answer as it is: the generic
 * "on its way", an installation's "not available", and the limiter's 429.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

const forgotPassword = vi.fn()
vi.mock('@/services/authService', () => ({ forgotPassword: (...a: unknown[]) => forgotPassword(...a) }))

import ForgotPasswordPage from './ForgotPasswordPage'

const apiError = (status: number, message: string) => Object.assign(new Error(message), { response: { status, data: { error: 'X', message, status } } })

const request = async (email = 'me@example.com') => {
  render(<MemoryRouter><ForgotPasswordPage /></MemoryRouter>)
  fireEvent.change(screen.getByLabelText(/email/i), { target: { value: email } })
  fireEvent.click(screen.getByRole('button', { name: /send reset link/i }))
}

// Braces matter: a hook that RETURNS the mock hands vitest a teardown function
// that calls it once more, with nobody awaiting the rejection.
beforeEach(() => { forgotPassword.mockClear() })

describe('ForgotPasswordPage', () => {
  it('sends the trimmed email and shows the generic answer', async () => {
    forgotPassword.mockResolvedValue({ success: true, message: 'If that address has an account, a password reset link is on its way' })
    await request('  me@example.com ')
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/on its way/))
    expect(forgotPassword).toHaveBeenCalledWith({ email: 'me@example.com' })
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('shows the installation\'s "not available" answer', async () => {
    forgotPassword.mockRejectedValue(apiError(503, 'password reset is not available on this installation yet — ask your workspace owner to set your password'))
    await request()
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/not available on this installation/))
  })

  it('shows the limiter\'s answer and keeps the form', async () => {
    forgotPassword.mockRejectedValue(apiError(429, 'too many failed sign-in attempts — wait a few minutes and try again'))
    await request()
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/wait a few minutes/))
    expect(screen.getByRole('button', { name: /send reset link/i })).toBeInTheDocument()
  })
})
