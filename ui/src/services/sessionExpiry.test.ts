/**
 * Session expiry: a 401 ends the session only when the server confirms the
 * session is gone — never on the 401 alone, and never on a network failure.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'

const getMe = vi.fn()
let token: string | null = 'tok'
vi.mock('@/services/authService', () => ({
  getMe: () => getMe(),
  hasSessionToken: () => !!token,
}))

import { reportUnauthorized, setSessionExpiredHandler } from './sessionExpiry'

const handler = vi.fn()

beforeEach(() => {
  token = 'tok'
  getMe.mockReset()
  handler.mockReset()
  setSessionExpiredHandler(handler)
})

describe('reportUnauthorized', () => {
  it('ends the session when the server no longer knows it', async () => {
    // What authService.getMe does on its own 401: drop the token, return null.
    getMe.mockImplementation(async () => { token = null; return null })
    await reportUnauthorized()
    expect(handler).toHaveBeenCalledTimes(1)
  })

  it('leaves the session alone when the 401 was about something else', async () => {
    getMe.mockResolvedValue({ user: { id: 'u1' } })
    await reportUnauthorized()
    expect(getMe).toHaveBeenCalledTimes(1)
    expect(handler).not.toHaveBeenCalled()
  })

  it('does not log anyone out over a network failure', async () => {
    // getMe returns null but keeps the token when it could not reach the server.
    getMe.mockResolvedValue(null)
    await reportUnauthorized()
    expect(handler).not.toHaveBeenCalled()
  })

  it('checks once for a burst of 401s', async () => {
    let release: (v: null) => void = () => {}
    getMe.mockImplementation(() => new Promise<null>((resolve) => { release = (v) => { token = null; resolve(v) } }))
    const all = Promise.all([reportUnauthorized(), reportUnauthorized(), reportUnauthorized()])
    release(null)
    await all
    expect(getMe).toHaveBeenCalledTimes(1)
    expect(handler).toHaveBeenCalledTimes(1)
    // …and the next 401, later, is checked again.
    token = 'tok2'
    getMe.mockResolvedValue({ user: { id: 'u1' } })
    await reportUnauthorized()
    expect(getMe).toHaveBeenCalledTimes(2)
  })

  it('does nothing when there is no session to lose', async () => {
    token = null
    await reportUnauthorized()
    expect(getMe).not.toHaveBeenCalled()
    expect(handler).not.toHaveBeenCalled()
  })
})
