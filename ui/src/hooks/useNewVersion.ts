/**
 * Notices when a newer UI is deployed than the one this tab runs (#551).
 *
 * The tab compares the hashed `/assets/` files it loaded with those in a
 * fresh `index.html` (nginx serves it uncached). It checks every few minutes
 * while visible, and when the tab comes back into view after a while. It
 * never reloads by itself; the caller shows a banner and the user decides.
 *
 * A new asset set is reported only once two checks at least
 * `VERSION_CONFIRM_GAP_MS` apart have seen the same one (#600). During a
 * rolling update old and new UI pods serve side by side, and a Reload that
 * lands on a new pod's `index.html` while an old pod answers for its script
 * gives a blank page. Waiting for a second, matching check lets the rollout
 * finish first.
 */

import { useEffect, useState } from 'react'
import { buildAssets, buildAssetsFromHtml } from '../utils/buildAssets'

export const VERSION_POLL_MS = 5 * 60_000
export const VERSION_FOCUS_GAP_MS = 60_000
/** Least time between the two checks that must agree on a new version. */
export const VERSION_CONFIRM_GAP_MS = 30_000
/** After a first sighting, the confirming check runs this long after it. */
export const VERSION_RECHECK_MS = VERSION_CONFIRM_GAP_MS + 1_000

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
    // A new set seen once, waiting for a second check to agree.
    let candidate: { assets: string; at: number } | null = null
    let recheck: number | undefined
    // The set the banner is currently offered for.
    let confirmed: string | null = null

    // No in-flight guard: a request that never settles must not stop later
    // checks, and the one-minute gap already keeps them from piling up.
    const check = async () => {
      lastCheck = Date.now()
      try {
        const res = await fetch(`${import.meta.env.BASE_URL}index.html?v=${Date.now()}`, {
          cache: 'no-store',
          credentials: 'same-origin',
        })
        if (!res.ok) {
          candidate = null
          return
        }
        const deployed = buildAssetsFromHtml(await res.text())
        if (cancelled) return
        if (!deployed) {
          candidate = null
          return
        }
        if (deployed === current) {
          candidate = null
          confirmed = null
          setNewer(null)
          return
        }
        // Already offered; a failed check in between doesn't make it flicker.
        if (deployed === confirmed) return
        const now = Date.now()
        if (candidate?.assets === deployed) {
          if (now - candidate.at >= VERSION_CONFIRM_GAP_MS) {
            confirmed = deployed
            setNewer(deployed)
          }
          return
        }
        // First sighting, or the set changed again: not offered yet, and a
        // version shown for an earlier set is withdrawn.
        candidate = { assets: deployed, at: now }
        confirmed = null
        setNewer(null)
        window.clearTimeout(recheck)
        recheck = window.setTimeout(onTick, VERSION_RECHECK_MS)
      } catch {
        // Offline, parked cluster, rollout: try again on the next check. A
        // sighting before it can't count as the previous of two in a row.
        candidate = null
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
      window.clearTimeout(recheck)
      document.removeEventListener('visibilitychange', onReturn)
      window.removeEventListener('focus', onReturn)
    }
  }, [])

  return newer
}
