/**
 * Notices when a newer UI is deployed than the one this tab runs (#551).
 *
 * The tab compares the hashed `/assets/` files it loaded with those in a
 * fresh `index.html` (nginx serves it uncached). It checks every few minutes
 * while visible, and when the tab comes back into view after a while. It
 * never reloads by itself; the caller shows a banner and the user decides.
 */

import { useEffect, useState } from 'react'
import { buildAssets, buildAssetsFromHtml } from '../utils/buildAssets'

export const VERSION_POLL_MS = 5 * 60_000
export const VERSION_FOCUS_GAP_MS = 60_000

/**
 * The asset set of a newer deployed UI, or null while the deployed UI is the
 * one this tab runs (or nothing could be read).
 */
export function useNewVersion(): string | null {
  const [newer, setNewer] = useState<string | null>(null)

  useEffect(() => {
    const current = buildAssets(document)
    // The dev server and tests load /src/main.tsx: nothing to compare.
    if (!current) return

    let cancelled = false
    let lastCheck = Date.now()

    // No in-flight guard: a request that never settles must not stop later
    // checks, and the one-minute gap already keeps them from piling up.
    const check = async () => {
      lastCheck = Date.now()
      try {
        const res = await fetch(`${import.meta.env.BASE_URL}index.html?v=${Date.now()}`, {
          cache: 'no-store',
          credentials: 'same-origin',
        })
        if (!res.ok) return
        const deployed = buildAssetsFromHtml(await res.text())
        if (cancelled || !deployed) return
        setNewer(deployed === current ? null : deployed)
      } catch {
        // Offline, parked cluster, rollout: try again on the next check.
      }
    }

    const onTick = () => {
      if (document.visibilityState === 'visible') void check()
    }
    const onReturn = () => {
      if (document.visibilityState !== 'visible') return
      if (Date.now() - lastCheck >= VERSION_FOCUS_GAP_MS) void check()
    }

    const timer = window.setInterval(onTick, VERSION_POLL_MS)
    document.addEventListener('visibilitychange', onReturn)
    window.addEventListener('focus', onReturn)
    return () => {
      cancelled = true
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', onReturn)
      window.removeEventListener('focus', onReturn)
    }
  }, [])

  return newer
}
