/**
 * Session expiry — the one place that decides "the session is gone".
 *
 * A session lasts 24 hours. A tab left open past that keeps its pages, and
 * without this each of them shows its own unrelated error ("Failed to rotate
 * API key") for what is simply a needed login. Anything that receives a 401
 * calls reportUnauthorized(); the auth store registers what happens next
 * (clear the session, which sends the user to the login page).
 *
 * It lives in its own module because the shared API client must be able to
 * report a 401 without importing the auth store, which imports the client.
 */

import * as authService from '@/services/authService'

type Handler = () => void

let handler: Handler | null = null
let checking: Promise<void> | null = null

/** Registered once by the auth store. */
export function setSessionExpiredHandler(fn: Handler | null): void {
  handler = fn
}

/**
 * Called by anything that got a 401. A 401 alone does not end the session —
 * it can also mean "this particular thing refused you" — so the server is
 * asked whether the session still stands (GET /auth/me). Only when that says
 * no is the handler called. Concurrent reports share one check.
 */
export function reportUnauthorized(): Promise<void> {
  if (!authService.hasSessionToken()) return Promise.resolve() // no session to lose
  if (checking) return checking
  checking = (async () => {
    try {
      const me = await authService.getMe()
      // getMe drops the token only on a 401 of its own. A network failure
      // or a 5xx leaves it in place: that is not proof of an expired session.
      if (!me && !authService.hasSessionToken()) handler?.()
    } finally {
      checking = null
    }
  })()
  return checking
}
