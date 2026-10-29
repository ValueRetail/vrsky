/**
 * The entry-script retry guard in index.html (#600).
 *
 * The guard is an inline classic script, so the test takes its source from
 * `index.html` and runs it in the jsdom window: a failing `/assets/` script
 * must reload the page once after a short wait, and never twice.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import indexHtml from '../../index.html?raw'
import { ASSET_RETRY_KEY, clearAssetRetry } from './assetRetry'

const reload = vi.fn()
const realLocation = window.location
let removeGuard: () => void = () => {}

/** The inline classic script in index.html's head. */
function guardSource(): string {
  const doc = new DOMParser().parseFromString(indexHtml, 'text/html')
  const inline = [...doc.head.querySelectorAll('script:not([src])')].filter(
    (s) => s.getAttribute('type') !== 'module',
  )
  expect(inline).toHaveLength(1)
  return inline[0].textContent ?? ''
}

/** Install the guard on this window; returns a way to take it off again. */
function installGuard() {
  const added: Array<[string, EventListenerOrEventListenerObject, boolean | AddEventListenerOptions | undefined]> = []
  const original = window.addEventListener.bind(window)
  vi.spyOn(window, 'addEventListener').mockImplementation((type, listener, options) => {
    added.push([type, listener as EventListenerOrEventListenerObject, options])
    original(type, listener, options)
  })
  new Function(guardSource())()
  vi.mocked(window.addEventListener).mockRestore()
  removeGuard = () => added.forEach(([t, l, o]) => window.removeEventListener(t, l, o))
}

/** A script element whose load failed, as the browser reports it. */
function failScript(src: string) {
  const el = document.createElement('script')
  el.setAttribute('type', 'module')
  el.setAttribute('src', src)
  document.head.appendChild(el)
  el.dispatchEvent(new Event('error')) // does not bubble, like the real one
  el.remove()
}

beforeEach(() => {
  vi.useFakeTimers()
  reload.mockReset()
  sessionStorage.clear()
  Object.defineProperty(window, 'location', {
    configurable: true,
    writable: true,
    value: { href: realLocation.href, origin: realLocation.origin, reload },
  })
  installGuard()
})

afterEach(() => {
  removeGuard()
  vi.useRealTimers()
  sessionStorage.clear()
  vi.restoreAllMocks()
  Object.defineProperty(window, 'location', { configurable: true, writable: true, value: realLocation })
})

describe('index.html', () => {
  it('has the guard before the entry module script', () => {
    const doc = new DOMParser().parseFromString(indexHtml, 'text/html')
    const scripts = [...doc.querySelectorAll('script')]
    const guard = scripts.findIndex((s) => !s.getAttribute('src') && s.getAttribute('type') !== 'module')
    const entry = scripts.findIndex((s) => s.getAttribute('type') === 'module' && s.getAttribute('src'))
    expect(guard).toBeGreaterThanOrEqual(0)
    expect(entry).toBeGreaterThan(guard)
  })

  it('uses the same sessionStorage key as the app', () => {
    expect(guardSource()).toContain(`'${ASSET_RETRY_KEY}'`)
  })
})

describe('asset retry guard', () => {
  it('reloads once, after a short wait, when a build script fails to load', () => {
    failScript('/assets/index-NEW.js')
    expect(reload).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1_000)
    expect(reload).not.toHaveBeenCalled()
    vi.advanceTimersByTime(2_000)
    expect(reload).toHaveBeenCalledTimes(1)
    expect(sessionStorage.getItem(ASSET_RETRY_KEY)).toBe('1')
  })

  it('does not loop: a second failure after the reload does not reload again', () => {
    failScript('/assets/index-NEW.js')
    vi.advanceTimersByTime(5_000)
    expect(reload).toHaveBeenCalledTimes(1)
    // The reloaded page fails the same way; the flag is still set.
    failScript('/assets/index-NEW.js')
    vi.advanceTimersByTime(60_000)
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('schedules a single reload however many scripts fail at once', () => {
    failScript('/assets/index-NEW.js')
    failScript('/assets/vendor-NEW.js')
    vi.advanceTimersByTime(5_000)
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('may retry again after the app has started and cleared the flag', () => {
    failScript('/assets/index-NEW.js')
    vi.advanceTimersByTime(5_000)
    clearAssetRetry()
    expect(sessionStorage.getItem(ASSET_RETRY_KEY)).toBeNull()
    failScript('/assets/index-NEWER.js')
    vi.advanceTimersByTime(5_000)
    expect(reload).toHaveBeenCalledTimes(2)
  })

  it('ignores scripts outside /assets/, such as an injected extension script', () => {
    failScript('/extension/inject.js')
    failScript('https://cdn.example.com/assets/x.js')
    vi.advanceTimersByTime(5_000)
    expect(reload).not.toHaveBeenCalled()
    expect(sessionStorage.getItem(ASSET_RETRY_KEY)).toBeNull()
  })

  it('ignores a failing image or other non-script element', () => {
    const img = document.createElement('img')
    img.setAttribute('src', '/assets/logo-123.png')
    document.body.appendChild(img)
    img.dispatchEvent(new Event('error'))
    img.remove()
    vi.advanceTimersByTime(5_000)
    expect(reload).not.toHaveBeenCalled()
  })

  it('does not reload when sessionStorage is unavailable (it could not stop a loop)', () => {
    vi.spyOn(sessionStorage, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    failScript('/assets/index-NEW.js')
    vi.advanceTimersByTime(5_000)
    expect(reload).not.toHaveBeenCalled()
  })
})

describe('clearAssetRetry', () => {
  it('removes the flag, and does not throw when sessionStorage does', () => {
    sessionStorage.setItem(ASSET_RETRY_KEY, '1')
    clearAssetRetry()
    expect(sessionStorage.getItem(ASSET_RETRY_KEY)).toBeNull()
    vi.spyOn(sessionStorage, 'removeItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    expect(() => clearAssetRetry()).not.toThrow()
  })
})
