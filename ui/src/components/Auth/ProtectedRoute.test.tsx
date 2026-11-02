/**
 * A failed /me at app load (no 401) shows a retry, not the login page (#611).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'

let state = { isAuthenticated: false, isInitialized: true, isLoading: false, loadFailed: false }
const checkAuth = vi.fn()
const logout = vi.fn()
vi.mock('@/store/authStore', () => ({
  useAuthStore: () => ({ ...state, checkAuth, logout }),
}))

import ProtectedRoute from './ProtectedRoute'

const show = () =>
  render(
    <MemoryRouter initialEntries={['/app']}>
      <Routes>
        <Route path="/login" element={<div>login page</div>} />
        <Route path="/app" element={<ProtectedRoute><div>secret</div></ProtectedRoute>} />
      </Routes>
    </MemoryRouter>,
  )

beforeEach(() => {
  state = { isAuthenticated: false, isInitialized: true, isLoading: false, loadFailed: false }
  checkAuth.mockReset()
  logout.mockReset()
})

describe('ProtectedRoute', () => {
  it('sends a signed-out visitor to the login page', () => {
    show()
    expect(screen.getByText('login page')).toBeInTheDocument()
  })

  it('shows a retry, not the login page, when /me could not be answered', () => {
    state.loadFailed = true
    show()
    expect(screen.queryByText('login page')).toBeNull()
    expect(screen.getByText(/Could not reach the server/)).toBeInTheDocument()
    checkAuth.mockClear()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(checkAuth).toHaveBeenCalledTimes(1)
  })

  it('offers "Sign in again" next to Retry, so a persistent failure is not a dead end', async () => {
    state.loadFailed = true
    logout.mockResolvedValue(undefined)
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Sign in again' }))
    expect(logout).toHaveBeenCalledTimes(1)
  })

  it('a failing logout call is not an unhandled rejection (vitest fails the run on one)', async () => {
    state.loadFailed = true
    logout.mockRejectedValue(new Error('network down'))
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Sign in again' }))
    await new Promise((r) => setTimeout(r, 10))
    expect(logout).toHaveBeenCalledTimes(1)
  })

  it('renders the page for a signed-in user', () => {
    state.isAuthenticated = true
    show()
    expect(screen.getByText('secret')).toBeInTheDocument()
  })
})
