/**
 * The reset page takes the token from the link, insists the two passwords
 * match, and shows the API's reason when the link is no good.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

const resetPassword = vi.fn()
vi.mock('@/services/authService', () => ({ resetPassword: (...a: unknown[]) => resetPassword(...a) }))

import ResetPasswordPage from './ResetPasswordPage'

const apiError = (status: number, message: string) => Object.assign(new Error(message), { response: { status, data: { error: 'X', message, status } } })

const show = (url = '/reset-password?token=tok-123') =>
  render(<MemoryRouter initialEntries={[url]}><ResetPasswordPage /></MemoryRouter>)

const fill = (a: string, b: string) => {
  fireEvent.change(screen.getByLabelText(/^new password/i), { target: { value: a } })
  fireEvent.change(screen.getByLabelText(/repeat/i), { target: { value: b } })
  fireEvent.click(screen.getByRole('button', { name: /set new password/i }))
}

beforeEach(() => { resetPassword.mockClear() })

describe('ResetPasswordPage', () => {
  it('sends the token from the link with the new password', async () => {
    resetPassword.mockResolvedValue({ success: true, message: 'Password reset successful. Please login with your new password.' })
    show()
    fill('a brand new passphrase', 'a brand new passphrase')
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/reset successful/i))
    expect(resetPassword).toHaveBeenCalledWith({ token: 'tok-123', new_password: 'a brand new passphrase' })
    expect(screen.getByRole('link', { name: 'Sign in' })).toBeInTheDocument()
  })

  it('refuses mismatched passwords without calling the API', () => {
    show()
    fill('a brand new passphrase', 'a different passphrase')
    expect(screen.getByRole('alert')).toHaveTextContent(/do not match/)
    expect(resetPassword).not.toHaveBeenCalled()
  })

  it('shows the API\'s reason for a bad link', async () => {
    resetPassword.mockRejectedValue(apiError(400, 'reset link has expired'))
    show()
    fill('a brand new passphrase', 'a brand new passphrase')
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/expired/))
    expect(screen.getByRole('link', { name: /request a new link/i })).toBeInTheDocument()
  })

  it('says so when the link has no token', () => {
    show('/reset-password')
    expect(screen.getByRole('alert')).toHaveTextContent(/missing its token/)
    expect(screen.getByRole('button', { name: /set new password/i })).toBeDisabled()
  })
})
