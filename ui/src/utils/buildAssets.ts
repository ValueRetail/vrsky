/**
 * The hashed build assets a page loads (#551). Vite names the entry script
 * and stylesheet `/assets/<name>-<hash>.<ext>`, so the set changes exactly
 * when the shipped UI does. Comparing the running tab's set with a fresh
 * `index.html` tells whether a newer UI is deployed.
 */

const ASSET_SELECTOR = 'script[src], link[rel="stylesheet"][href]'

/**
 * The `/assets/` URLs the page references, sorted and joined into one
 * string. Empty when there are none (the Vite dev server, tests, an ingress
 * error page). Anything outside this origin's `/assets/` is ignored, so a
 * script a browser extension injects into the live page doesn't count.
 */
export function buildAssets(root: ParentNode): string {
  const urls = new Set<string>()
  root.querySelectorAll(ASSET_SELECTOR).forEach((el) => {
    const raw = el.getAttribute(el.tagName === 'SCRIPT' ? 'src' : 'href')
    if (!raw) return
    let url: URL
    try {
      url = new URL(raw, window.location.origin)
    } catch {
      return
    }
    if (url.origin === window.location.origin && url.pathname.startsWith('/assets/')) {
      urls.add(url.pathname)
    }
  })
  return [...urls].sort().join('\n')
}

/** The same set read from HTML text, such as a fetched `index.html`. */
export function buildAssetsFromHtml(html: string): string {
  return buildAssets(new DOMParser().parseFromString(html, 'text/html'))
}
