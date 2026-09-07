import { describe, it, expect, afterEach, vi } from 'vitest'

/**
 * config.webhookIngressUrl is the base of the URL a partner is given to POST
 * to. Getting it wrong is not a cosmetic bug: the deployed UI handed out
 * "http://localhost:9100/webhook/<id>" — an address on the reader's own
 * machine — in the pipeline builder, the onboarding wizard, and the Webhook
 * (HTTP) source panel.
 *
 * It cannot be a fixed string, because Vite freezes VITE_* into the bundle at
 * build time and ui/Dockerfile builds once with no build args, so whatever is
 * hardcoded ships everywhere.
 *
 * config is evaluated at module load, so each case stubs the env and
 * re-imports.
 */
const loadConfig = async () => {
  vi.resetModules()
  return (await import('@/config/env')).config
}

describe('config.webhookIngressUrl', () => {
  const realLocation = window.location

  afterEach(() => {
    vi.unstubAllEnvs()
    vi.resetModules()
    Object.defineProperty(window, 'location', {
      value: realLocation,
      writable: true,
      configurable: true,
    })
  })

  it('uses the page origin in a built bundle', async () => {
    vi.stubEnv('DEV', false)
    vi.stubEnv('PROD', true)

    const config = await loadConfig()

    // The webhook ingress serves /webhook on the same host that serves this
    // UI, so the origin is the correct base — and it follows the host when it
    // changes (sslip.io today, real DNS later) with no rebuild.
    expect(config.webhookIngressUrl).toBe(window.location.origin)
  })

  it('follows the deployment host, and is not the local worker port', async () => {
    vi.stubEnv('DEV', false)
    vi.stubEnv('PROD', true)
    const origin = 'https://20.251.107.2.sslip.io'
    Object.defineProperty(window, 'location', {
      value: { ...window.location, origin, href: origin + '/' },
      writable: true,
      configurable: true,
    })

    const config = await loadConfig()

    // This is the regression itself. A deployed pipeline's webhook URL
    // pointing at the reader's own machine is silently useless: nothing
    // errors, the sender just never reaches the platform.
    expect(config.webhookIngressUrl).toBe(origin)
    expect(config.webhookIngressUrl).not.toBe('http://localhost:9100')
  })

  it('uses the local worker port in dev', async () => {
    vi.stubEnv('DEV', true)
    vi.stubEnv('PROD', false)

    const config = await loadConfig()

    // Dev is the one case where the origin is wrong: Vite serves the UI on
    // :5173 while webhook-consumer listens on :9100.
    expect(config.webhookIngressUrl).toBe('http://localhost:9100')
  })

  it('honours an explicit override', async () => {
    vi.stubEnv('DEV', false)
    vi.stubEnv('PROD', true)
    vi.stubEnv('VITE_WEBHOOK_INGRESS_URL', 'https://hooks.example.com')

    const config = await loadConfig()

    // Needed where webhooks enter on a different host than the UI.
    expect(config.webhookIngressUrl).toBe('https://hooks.example.com')
  })
})
