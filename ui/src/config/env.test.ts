/**
 * Environment config, and in particular the webhook base URL.
 *
 * defaultWebhookBase exists because a hardcoded default shipped
 * "http://localhost:9100/webhook/..." to production as a partner-facing URL —
 * Vite freezes VITE_* at build time and the UI image is built once, so a
 * ConfigMap could not correct it. The logic that replaced it had no test.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

const ORIGIN = 'https://vrsky.example.test'

// getEnv/defaultWebhookBase run at module load, so each case needs a fresh
// import with import.meta.env and window shaped for it.
async function loadConfig(env: Record<string, unknown>) {
  vi.resetModules()
  vi.stubGlobal('import.meta', undefined) // no-op guard; Vite handles the real one
  for (const [k, v] of Object.entries(env)) vi.stubEnv(k, v as string)
  return import('./env')
}

beforeEach(() => {
  vi.unstubAllEnvs()
  vi.resetModules()
})
afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

describe('webhookIngressUrl', () => {
  it('uses an explicit VITE_WEBHOOK_INGRESS_URL when set', async () => {
    const { config } = await loadConfig({ VITE_WEBHOOK_INGRESS_URL: 'https://hooks.partner.test' })
    expect(config.webhookIngressUrl).toBe('https://hooks.partner.test')
  })

  it('falls back to the page origin in a served deployment', async () => {
    // The regression this guards: the deployed UI handing a partner a
    // localhost URL. In a served deployment the webhook ingress is on the same
    // host as the UI, so the origin is correct and survives a host change with
    // no rebuild.
    //
    // Note the var is left UNSET rather than set to '': getEnv uses ??, so an
    // empty string is a value and would not reach the default at all.
    vi.stubGlobal('window', { location: { origin: ORIGIN } })
    const { config } = await loadConfig({ DEV: false, PROD: true, MODE: 'production' })
    expect(config.webhookIngressUrl).toBe(ORIGIN)
  })

  it('uses the compose port in local dev, where the origin is wrong', async () => {
    // Vite serves the UI on :5173 while webhook-consumer listens on :9100.
    vi.stubGlobal('window', { location: { origin: 'http://localhost:5173' } })
    const { config } = await loadConfig({ DEV: true, PROD: false, MODE: 'development' })
    expect(config.webhookIngressUrl).toBe('http://localhost:9100')
  })

  it('falls back to the compose port with no window at all', async () => {
    // Unit tests and any SSR context have no origin to read.
    vi.stubGlobal('window', undefined)
    const { config } = await loadConfig({ DEV: false, PROD: true, MODE: 'production' })
    expect(config.webhookIngressUrl).toBe('http://localhost:9100')
  })
})

describe('defaults', () => {
  it('apiUrl defaults to "" so requests stay same-origin', async () => {
    const { config } = await loadConfig({ VITE_API_URL: '' })
    expect(config.apiUrl).toBe('')
  })

  it('exposes no worker URLs: file endpoints are proxied by the API', async () => {
    // The worker aux ports have no tenant check of their own, so the browser
    // must not be able to address one. Guards against a VITE_FILE_*_URL
    // creeping back in the next time a panel needs a worker.
    const { config } = await loadConfig({})
    const keys = Object.keys(config)
    expect(keys.filter((k) => /^file(Producer|Consumer)/.test(k))).toEqual([])
  })

  it('exposes no separate stream URL: streams use the API origin', async () => {
    // config.wsUrl (VITE_WS_URL, default http://localhost:3000) fed only the
    // dead services/websocket.ts (#428). A stream on another origin carries
    // no session cookie in dev and points at the wrong host in production,
    // so live streams go through apiUrl like services/workerEvents.ts does.
    const { config } = await loadConfig({})
    expect(Object.keys(config).filter((k) => /^ws/i.test(k))).toEqual([])
  })

  it('applies the documented defaults when nothing is set', async () => {
    const { config } = await loadConfig({})
    expect(config.tenantId).toBeTruthy()
    expect(config.docsUrl).toBeTruthy() // the exact URL is tied to mkdocs.yml below
    expect(['debug', 'info', 'warn', 'error']).toContain(config.logLevel)
  })
})

describe('VITE_WS_URL is gone (#428)', () => {
  // Every UI source file and the example env, as text. Test files are left
  // out because this one has to name the variable to look for it.
  const sources = import.meta.glob(
    ['../**/*.{ts,tsx}', '!../**/*.test.{ts,tsx}', '../../.env.example'],
    { query: '?raw', import: 'default', eager: true },
  ) as Record<string, string>

  it('reads at least the files it means to check', () => {
    // A glob that matched nothing would make the checks below pass vacuously.
    expect(Object.keys(sources)).toContain('./env.ts')
    expect(Object.keys(sources)).toContain('../../.env.example')
  })

  it('no source file or example env mentions VITE_WS_URL', () => {
    const offenders = Object.entries(sources)
      .filter(([, text]) => text.includes('VITE_WS_URL'))
      .map(([file]) => file)
    expect(offenders).toEqual([])
  })
})

describe('validateConfig', () => {
  it('accepts an empty apiUrl — "" means relative, and is the normal case', async () => {
    const { validateConfig } = await loadConfig({ VITE_API_URL: '' })
    expect(() => validateConfig()).not.toThrow()
  })

  it('rejects a missing tenant id', async () => {
    const mod = await loadConfig({ VITE_TENANT_ID: 'tenant-1' })
    // config is a frozen-ish literal; exercise the guard directly.
    const original = mod.config.tenantId
    ;(mod.config as { tenantId: string }).tenantId = ''
    expect(() => mod.validateConfig()).toThrow('VITE_TENANT_ID')
    ;(mod.config as { tenantId: string }).tenantId = original
  })
})

describe('docs URL matches mkdocs.yml (#451)', () => {
  // The sidebar's Help & Docs link opens the published mkdocs site, so the UI
  // default and mkdocs.yml's site_url have to name the same place.
  const mkdocs = import.meta.glob('../../../mkdocs.yml', {
    query: '?raw',
    import: 'default',
    eager: true,
  }) as Record<string, string>

  it('uses mkdocs.yml site_url as the default docsUrl', async () => {
    const files = Object.values(mkdocs)
    expect(files).toHaveLength(1)
    // Same parsing as scripts/check-docs-url.sh: optional quotes, optional
    // trailing "# comment".
    const siteUrl = /^site_url:\s*(?:"([^"]*)"|'([^']*)'|([^\s#]+))\s*(?:#.*)?$/m
      .exec(files[0])
      ?.slice(1)
      .find(Boolean)
    expect(siteUrl).toBeTruthy()
    const { config } = await loadConfig({})
    expect(config.docsUrl).toBe(siteUrl)
  })
})
