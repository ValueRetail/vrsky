/**
 * Clears the one-shot reload flag the inline guard in `index.html` sets when
 * a build script fails to load (#600). Called once the app has started, so a
 * later rollout in the same tab can retry again.
 */

export const ASSET_RETRY_KEY = 'vrsky:asset-retry'

export function clearAssetRetry(): void {
  try {
    sessionStorage.removeItem(ASSET_RETRY_KEY)
  } catch {
    // sessionStorage unavailable: the guard never set a flag either.
  }
}
