/**
 * The "new version available" banner (#551): shown when a newer UI is
 * deployed, Reload only on a click, Later hides it until a different version.
 *
 * Runs the real hook: the tab's assets go into <head>, `fetch` answers with
 * an index.html, timers are fake.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, act, fireEvent } from '@testing-library/react'
import NewVersionBanner from './NewVersionBanner'
import { VERSION_POLL_MS } from '../../hooks/useNewVersion'

const A = ['/assets/index-AAA.js', '/assets/index-AAA.css']
const B = ['/assets/index-BBB.js', '/assets/index-BBB.css']
const C = ['/assets/index-CCC.js', '/assets/index-CCC.css']

const html = (assets: string[]) =>
  `<!doctype html><html><head>${assets
    .map((a) => (a.endsWith('.css') ? `<link rel="stylesheet" href="${a}">` : `<script type="module" src="${a}"></script>`))
    .join('')}</head><body><div id="root"></div></body></html>`

const answer = (body: string, status = 200) => ({ ok: status >= 200 && status < 300, status, text: async () => body })

const fetchMock = vi.fn()
const reload = vi.fn()
const realLocation = window.location
const added: Element[] = []

function loadTab(assets: string[]) {
  for (const a of assets) {
    const el = document.createElement(a.endsWith('.css') ? 'link' : 'script')
    if (a.endsWith('.css')) {
      el.setAttribute('rel', 'stylesheet')
      el.setAttribute('href', a)
    } else {
      el.setAttribute('type', 'module')
      el.setAttribute('src', a)
    }
    document.head.appendChild(el)
    added.push(el)
  }
}

async function poll() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(VERSION_POLL_MS)
  })
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
}

const banner = () => screen.queryByText(/new version of VRSky is available/i)

beforeEach(() => {
  vi.useFakeTimers()
  fetchMock.mockReset()
  reload.mockReset()
  vi.stubGlobal('fetch', fetchMock)
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' })
  Object.defineProperty(window, 'location', {
    configurable: true,
    writable: true,
    value: { ...realLocation, origin: realLocation.origin, href: realLocation.href, reload },
  })
  loadTab(A)
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  added.splice(0).forEach((el) => el.remove())
  delete (document as unknown as Record<string, unknown>).visibilityState
  Object.defineProperty(window, 'location', { configurable: true, writable: true, value: realLocation })
})

describe('NewVersionBanner', () => {
  it('shows nothing before any check', () => {
    fetchMock.mockResolvedValue(answer(html(B)))
    const { container } = render(<NewVersionBanner />)
    expect(container).toBeEmptyDOMElement()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('says a new version is available and offers Reload and Later when the deployed assets differ', async () => {
    fetchMock.mockResolvedValue(answer(html(B)))
    render(<NewVersionBanner />)
    await poll()
    expect(screen.getByRole('status')).toHaveTextContent(/new version of VRSky is available/i)
    expect(screen.getByRole('button', { name: 'Reload' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Later' })).toBeInTheDocument()
  })

  it('shows nothing when the deployed assets are the tab’s own', async () => {
    fetchMock.mockResolvedValue(answer(html(A)))
    const { container } = render(<NewVersionBanner />)
    await poll()
    await poll()
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(container).toBeEmptyDOMElement()
  })

  it('shows nothing for a failed fetch, a non-2xx answer or a page without assets', async () => {
    fetchMock
      .mockRejectedValueOnce(new TypeError('Failed to fetch'))
      .mockResolvedValueOnce(answer(html(B), 503))
      .mockResolvedValueOnce(answer('<html><body>502 Bad Gateway</body></html>'))
      .mockResolvedValueOnce(answer(html(B)))
    const { container } = render(<NewVersionBanner />)
    await poll()
    await poll()
    await poll()
    expect(container).toBeEmptyDOMElement()
    await poll()
    expect(banner()).toBeInTheDocument()
  })

  it('reloads the page on Reload, and never without a click', async () => {
    fetchMock.mockResolvedValue(answer(html(B)))
    render(<NewVersionBanner />)
    await poll()
    await poll()
    await poll()
    expect(banner()).toBeInTheDocument()
    expect(reload).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Reload' }))
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('Later hides the banner for that version, without reloading', async () => {
    fetchMock.mockResolvedValue(answer(html(B)))
    render(<NewVersionBanner />)
    await poll()
    fireEvent.click(screen.getByRole('button', { name: 'Later' }))
    expect(banner()).not.toBeInTheDocument()
    // The same version on later checks stays dismissed.
    await poll()
    await poll()
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(banner()).not.toBeInTheDocument()
    expect(reload).not.toHaveBeenCalled()
  })

  it('shows the banner again for a later, different version', async () => {
    fetchMock
      .mockResolvedValueOnce(answer(html(B)))
      .mockResolvedValueOnce(answer(html(B)))
      .mockResolvedValueOnce(answer(html(C)))
    render(<NewVersionBanner />)
    await poll()
    fireEvent.click(screen.getByRole('button', { name: 'Later' }))
    await poll()
    expect(banner()).not.toBeInTheDocument()
    await poll()
    expect(banner()).toBeInTheDocument()
  })
})
