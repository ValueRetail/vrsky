/**
 * Session-token handling — 3.4% statements, 0% branches before this.
 *
 * The branches here decide whether an invalid token is cleared and whether a
 * logout that fails server-side still ends the local session. Both fail
 * quietly if broken: the user appears logged in against a token the server
 * rejects.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import axios from 'axios'

const client = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), delete: vi.fn() }))
vi.mock('axios', async (orig) => {
  const actual = await orig<typeof import('axios')>()
  return {
    ...actual,
    default: { ...actual.default, create: () => client, isAxiosError: actual.default.isAxiosError },
  }
})

import {
  getSessionToken,
  setSessionToken,
  clearSessionToken,
  hasSessionToken,
  getMe,
  login,
  logout,
  register,
} from './authService'

const axiosErr = (status: number, data: unknown = {}) =>
  Object.assign(new Error('request failed'), {
    isAxiosError: true,
    response: { status, data },
    config: {},
    toJSON: () => ({}),
  })

beforeEach(() => {
  vi.clearAllMocks()
  sessionStorage.clear()
})

describe('token storage', () => {
  it('round-trips a token', () => {
    expect(getSessionToken()).toBeNull()
    expect(hasSessionToken()).toBe(false)

    setSessionToken('tok-1')

    expect(getSessionToken()).toBe('tok-1')
    expect(hasSessionToken()).toBe(true)
  })

  it('clears it', () => {
    setSessionToken('tok-1')
    clearSessionToken()
    expect(getSessionToken()).toBeNull()
    expect(hasSessionToken()).toBe(false)
  })
})

describe('getMe', () => {
  it('returns null without a token, and makes no request', async () => {
    await expect(getMe()).resolves.toBeNull()
    expect(client.get).not.toHaveBeenCalled()
  })

  it('sends the token as a bearer credential', async () => {
    setSessionToken('tok-1')
    client.get.mockResolvedValue({ data: { user: { id: 'u1' } } })

    await expect(getMe()).resolves.toEqual({ user: { id: 'u1' } })
    expect(client.get).toHaveBeenCalledWith('/api/v1/auth/me', {
      headers: { Authorization: 'Bearer tok-1' },
    })
  })

  it('clears the token on 401 — an invalid session must not look valid', async () => {
    setSessionToken('stale')
    vi.spyOn(axios, 'isAxiosError').mockReturnValue(true)
    client.get.mockRejectedValue(axiosErr(401))

    await expect(getMe()).resolves.toBeNull()
    expect(getSessionToken()).toBeNull()
  })

  it('keeps the token on a 500 — a server blip is not a bad credential', async () => {
    setSessionToken('good')
    vi.spyOn(axios, 'isAxiosError').mockReturnValue(true)
    client.get.mockRejectedValue(axiosErr(500))

    await expect(getMe()).resolves.toBeNull()
    expect(getSessionToken()).toBe('good')
  })
})

describe('logout', () => {
  it('does nothing without a token', async () => {
    await logout()
    expect(client.post).not.toHaveBeenCalled()
  })

  it('clears the token even when the server call fails', async () => {
    // The `finally`. Without it a failed logout leaves the user holding a
    // token they believe is gone.
    setSessionToken('tok-1')
    client.post.mockRejectedValue(new Error('network down'))

    await expect(logout()).rejects.toThrow('network down')
    expect(getSessionToken()).toBeNull()
  })

  it('clears the token on success', async () => {
    setSessionToken('tok-1')
    client.post.mockResolvedValue({})

    await logout()

    expect(client.post).toHaveBeenCalledWith('/api/v1/auth/logout', {}, {
      headers: { Authorization: 'Bearer tok-1' },
    })
    expect(getSessionToken()).toBeNull()
  })
})

describe('login', () => {
  it('stores the session token on success', async () => {
    client.post.mockResolvedValue({ data: { success: true, session_token: 'tok-new' } })

    await expect(login({ email: 'a@b.test', password: 'pw' })).resolves.toMatchObject({ success: true })
    expect(getSessionToken()).toBe('tok-new')
  })

  it('stores nothing when the response succeeds without a token', async () => {
    client.post.mockResolvedValue({ data: { success: true } })

    await login({ email: 'a@b.test', password: 'pw' })

    expect(getSessionToken()).toBeNull()
  })

  // Each status maps to guidance the user can act on. Collapsing them to one
  // generic message is a silent regression: the "verify your email" case looks
  // identical to a wrong password.
  it.each([
    [401, /invalid email or password/i],
    [403, /not verified/i],
    [500, /login failed \(error 500\)/i],
  ])('turns a %i into an actionable message, without throwing', async (status, expected) => {
    vi.spyOn(axios, 'isAxiosError').mockReturnValue(true)
    client.post.mockRejectedValue(axiosErr(status as number))

    const res = await login({ email: 'a@b.test', password: 'pw' })

    expect(res.success).toBe(false)
    expect(res.message).toMatch(expected as RegExp)
    expect(getSessionToken()).toBeNull()
  })

  it('prefers the server-supplied message when there is one', async () => {
    vi.spyOn(axios, 'isAxiosError').mockReturnValue(true)
    client.post.mockRejectedValue(axiosErr(401, { message: 'Account locked' }))

    await expect(login({ email: 'a@b.test', password: 'pw' })).resolves.toMatchObject({
      success: false,
      message: 'Account locked',
    })
  })

  it('rethrows a non-HTTP failure rather than reporting a login failure', async () => {
    // A network error is not a credential problem, and telling the user their
    // password is wrong would send them somewhere useless.
    vi.spyOn(axios, 'isAxiosError').mockReturnValue(false)
    client.post.mockRejectedValue(new Error('network down'))

    await expect(login({ email: 'a@b.test', password: 'pw' })).rejects.toThrow('network down')
  })
})

describe('register', () => {
  it('returns the server payload on success', async () => {
    client.post.mockResolvedValue({ data: { success: true, message: 'check your email' } })

    await expect(register({ email: 'a@b.test', password: 'pw' } as never)).resolves.toMatchObject({
      success: true,
    })
  })

  it('reports an HTTP failure without throwing', async () => {
    vi.spyOn(axios, 'isAxiosError').mockReturnValue(true)
    client.post.mockRejectedValue(axiosErr(409, { message: 'Email already registered' }))

    await expect(register({ email: 'a@b.test', password: 'pw' } as never)).resolves.toMatchObject({
      success: false,
      message: 'Email already registered',
    })
  })

  it('rethrows a non-HTTP failure', async () => {
    vi.spyOn(axios, 'isAxiosError').mockReturnValue(false)
    client.post.mockRejectedValue(new Error('dns failure'))

    await expect(register({ email: 'a@b.test', password: 'pw' } as never)).rejects.toThrow('dns failure')
  })
})
