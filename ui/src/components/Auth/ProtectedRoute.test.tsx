/**
 * A failed /me at app load (no 401) shows a retry, not the login page (#611).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'

let state = { isAuthenticated: false, isInitialized: true, isLoading: false, loadFailed: false }
const checkAuth = vi.fn()
vi.mock('@/store/authStore', () => ({
  useAuthStore: () => ({ ...state, checkAuth }),
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

  it('renders the page for a signed-in user', () => {
    state.isAuthenticated = true
    show()
    expect(screen.getByText('secret')).toBeInTheDocument()
  })
})
