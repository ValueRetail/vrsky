/**
 * Tells the user a newer UI is deployed (#551). A tab left open across
 * deploys otherwise keeps running the old bundle against the new API. The
 * banner never reloads by itself: the canvas saves locally a moment after
 * each edit, and the user picks the moment.
 */

import { useState } from 'react'
import { useNewVersion } from '../../hooks/useNewVersion'

export default function NewVersionBanner() {
  const newer = useNewVersion()
  const [dismissed, setDismissed] = useState<string | null>(null)

  if (!newer || newer === dismissed) return null

  return (
    <div
      role="status"
      className="fixed bottom-4 inset-x-4 sm:inset-x-0 sm:mx-auto sm:w-fit z-40 flex items-center gap-4 rounded-lg border border-primary-200 dark:border-primary-800 bg-white dark:bg-neutral-800 px-4 py-3 text-sm text-neutral-800 dark:text-neutral-100 shadow-lg"
    >
      <span className="flex-1">A new version of VRSky is available.</span>
      <button
        type="button"
        onClick={() => window.location.reload()}
        className="font-semibold text-primary-600 dark:text-primary-400 underline whitespace-nowrap"
      >
        Reload
      </button>
      <button
        type="button"
        onClick={() => setDismissed(newer)}
        className="text-neutral-500 dark:text-neutral-400 whitespace-nowrap"
      >
        Later
      </button>
    </div>
  )
}
