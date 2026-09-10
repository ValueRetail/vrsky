import { afterEach, vi } from 'vitest'
import { cleanup } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

// Cleanup after each test
afterEach(() => {
  cleanup()
  memoryStorage.clear()
})

// localStorage polyfill.
//
// jsdom provides localStorage, but Node 26 ships its own experimental one that
// is only enabled with --localstorage-file, and it SHADOWS jsdom's. The result
// is a environment where `window` and `document` are real but `localStorage` is
// undefined — so anything touching storage throws, and Node prints only a
// tangential warning about --localstorage-file.
//
// The app uses localStorage for the theme and the per-tenant onboarding flag,
// so tests need a working one. This is a plain in-memory Storage, reset between
// tests by the cleanup below.
class MemoryStorage implements Storage {
  #store = new Map<string, string>()
  get length(): number { return this.#store.size }
  key(i: number): string | null { return Array.from(this.#store.keys())[i] ?? null }
  getItem(k: string): string | null { return this.#store.get(k) ?? null }
  setItem(k: string, v: string): void { this.#store.set(String(k), String(v)) }
  removeItem(k: string): void { this.#store.delete(k) }
  clear(): void { this.#store.clear() }
}

const memoryStorage = new MemoryStorage()
for (const target of [globalThis, window] as const) {
  Object.defineProperty(target, 'localStorage', {
    configurable: true,
    writable: true,
    value: memoryStorage,
  })
}

// Mock window.matchMedia
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: vi.fn().mockImplementation(query => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })),
})

// Mock IntersectionObserver
global.IntersectionObserver = class IntersectionObserver {
  constructor() {}
  disconnect() {}
  observe() {}
  takeRecords() {
    return []
  }
  unobserve() {}
}
