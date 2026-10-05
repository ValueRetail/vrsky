/**
 * The login page tells a user whose session ran out why they are here.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'

let state = { sessionExpired: false, error: null as string | null }
const login = vi.fn()
const clearError = vi.fn()
vi.mock('@/store/authStore', () => ({
  useAuthStore: () => ({
    login, isAuthenticated: false, isLoading: false, clearError,
    error: state.error, sessionExpired: state.sessionExpired,
  }),
}))

import LoginPage from './LoginPage'

const show = () => render(<MemoryRouter><LoginPage /></MemoryRouter>)

beforeEach(() => {
  state = { sessionExpired: false, error: null }
  login.mockReset()
})

describe('LoginPage session-expired notice', () => {
  it('is shown after the session ran out', () => {
    state.sessionExpired = true
    show()
    expect(screen.getByRole('status')).toHaveTextContent('Your session has expired')
  })

  it('is absent on an ordinary visit, and gives way to a login error', () => {
    show()
    expect(screen.queryByText(/session has expired/)).toBeNull()
    state = { sessionExpired: true, error: 'Invalid email or password' }
    show()
    expect(screen.queryByText(/session has expired/)).toBeNull()
    expect(screen.getByText(/Invalid email or password/)).toBeInTheDocument()
  })
})

describe('LoginPage sign-in', () => {
  // The second half of the expiry story: the route guard remembered where
  // the user was, and a successful login takes them back there.
  it('returns to the page the user was sent away from', async () => {
    state.sessionExpired = true
    login.mockResolvedValue(true)
    render(
      <MemoryRouter initialEntries={[{ pathname: '/login', state: { from: '/settings/api-key' } }]}>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route path="/settings/api-key" element={<p>the api key page</p>} />
          <Route path="/" element={<p>home</p>} />
        </Routes>
      </MemoryRouter>,
    )
    fireEvent.change(screen.getByLabelText(/Email/i), { target: { value: 'a@b.c' } })
    fireEvent.change(screen.getByLabelText(/Password/i), { target: { value: 'pw' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await waitFor(() => expect(login).toHaveBeenCalledWith('a@b.c', 'pw'))
    expect(await screen.findByText('the api key page')).toBeInTheDocument()
  })

  it('stays put on a failed login and asks for both fields', async () => {
    login.mockResolvedValue(false)
    const { container } = show()
    // The inputs are `required`, so a click never submits an empty form;
    // submit it directly to reach the page's own check.
    fireEvent.submit(container.querySelector('form')!)
    expect(await screen.findByText(/Email and password are required/)).toBeInTheDocument()
    expect(login).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText(/Email/i), { target: { value: 'a@b.c' } })
    fireEvent.change(screen.getByLabelText(/Password/i), { target: { value: 'wrong' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await waitFor(() => expect(login).toHaveBeenCalled())
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument()
  })
})
