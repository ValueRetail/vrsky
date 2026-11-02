import { describe, it, expect } from 'vitest'

// Guard for #650: the production build must not write source maps. A map
// next to the bundle serves the original TypeScript to anyone who asks for
// assets/*.js.map. The dev server does not read build.sourcemap, so turning
// it off changes nothing locally.
//
// The config is read as text, not imported: importing vite.config.ts loads
// vite and esbuild inside the jsdom environment, and esbuild's invariant
// check fails there on Node 26.
const configSource = import.meta.glob('/vite.config.ts', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

describe('production build', () => {
  const source = Object.values(configSource)[0] ?? ''

  it('reads the vite config', () => {
    expect(source).toContain('defineConfig')
  })

  it('does not write source maps', () => {
    expect(source).toMatch(/sourcemap:\s*false/)
    expect(source).not.toMatch(/sourcemap:\s*(true|'inline'|'hidden')/)
  })
})
