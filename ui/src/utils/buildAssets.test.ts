/**
 * The hashed build assets a page loads (#551): what counts as the UI's
 * version, read from the live document or from a fetched index.html.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { buildAssets, buildAssetsFromHtml } from './buildAssets'

const page = (head: string) => `<!doctype html><html><head>${head}</head><body><div id="root"></div></body></html>`

// What `vite build` writes into index.html.
const VITE_HEAD = `
  <link rel="icon" type="image/svg+xml" href="/vite.svg" />
  <script type="module" crossorigin src="/assets/index-AAA.js"></script>
  <link rel="stylesheet" crossorigin href="/assets/index-AAA.css">`

describe('buildAssetsFromHtml', () => {
  it('reads the entry script and stylesheet of a built index.html', () => {
    expect(buildAssetsFromHtml(page(VITE_HEAD))).toBe('/assets/index-AAA.css\n/assets/index-AAA.js')
  })

  it('changes when a hash changes', () => {
    const next = VITE_HEAD.replace('index-AAA.js', 'index-BBB.js')
    expect(buildAssetsFromHtml(page(next))).not.toBe(buildAssetsFromHtml(page(VITE_HEAD)))
  })

  it('ignores references on another origin, even under /assets/', () => {
    const head = `
      <script src="https://cdn.example.com/assets/index-EXT.js"></script>
      <script src="//other.example.net/assets/inject.js"></script>
      <link rel="stylesheet" href="https://cdn.example.com/assets/theme.css">
      <script src="/assets/index-AAA.js"></script>`
    expect(buildAssetsFromHtml(page(head))).toBe('/assets/index-AAA.js')
  })

  it('counts an absolute URL on this origin like a relative one', () => {
    const abs = page(`<script src="${window.location.origin}/assets/index-AAA.js"></script>`)
    const rel = page('<script src="/assets/index-AAA.js"></script>')
    expect(buildAssetsFromHtml(abs)).toBe(buildAssetsFromHtml(rel))
    expect(buildAssetsFromHtml(abs)).toBe('/assets/index-AAA.js')
  })

  it('ignores same-origin references outside /assets/', () => {
    const head = `
      <script type="module" src="/src/main.tsx"></script>
      <script src="/@vite/client"></script>
      <script src="/assets-old/index-OLD.js"></script>
      <link rel="stylesheet" href="/styles/site.css">
      <link rel="icon" href="/assets/favicon.svg">`
    expect(buildAssetsFromHtml(page(head))).toBe('')
  })

  it('is empty for the Vite dev page and for an error page', () => {
    expect(buildAssetsFromHtml(page('<script type="module" src="/src/main.tsx"></script>'))).toBe('')
    expect(buildAssetsFromHtml('<html><body><h1>503 Service Temporarily Unavailable</h1></body></html>')).toBe('')
    expect(buildAssetsFromHtml('')).toBe('')
  })

  it('does not depend on the order of the references', () => {
    const a = page('<script src="/assets/a-1.js"></script><link rel="stylesheet" href="/assets/b-2.css"><script src="/assets/c-3.js"></script>')
    const b = page('<script src="/assets/c-3.js"></script><script src="/assets/a-1.js"></script><link rel="stylesheet" href="/assets/b-2.css">')
    expect(buildAssetsFromHtml(a)).toBe(buildAssetsFromHtml(b))
  })

  it('collapses duplicate references', () => {
    const dup = page(`
      <script src="/assets/index-AAA.js"></script>
      <script src="/assets/index-AAA.js"></script>
      <script src="${window.location.origin}/assets/index-AAA.js"></script>`)
    expect(buildAssetsFromHtml(dup)).toBe('/assets/index-AAA.js')
  })
})

describe('buildAssets(document)', () => {
  const added: Element[] = []
  afterEach(() => {
    added.splice(0).forEach((el) => el.remove())
  })

  const add = (html: string) => {
    const tpl = document.createElement('template')
    tpl.innerHTML = html
    for (const el of Array.from(tpl.content.children)) {
      document.head.appendChild(el)
      added.push(el)
    }
  }

  it('is empty for a document without built assets', () => {
    expect(buildAssets(document)).toBe('')
  })

  it('reads the live document, ignoring a script an extension injected', () => {
    add(VITE_HEAD)
    add('<script src="chrome-extension://abcdef/content.js"></script>')
    add('<script src="https://ext.example.com/assets/inject.js"></script>')
    expect(buildAssets(document)).toBe('/assets/index-AAA.css\n/assets/index-AAA.js')
  })
})
