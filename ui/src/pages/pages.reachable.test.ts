import ts from 'typescript'
import { describe, expect, it } from 'vitest'

// Guard for #513: every page under src/pages must be imported by some other
// non-test source file, otherwise it is dead code nobody can reach (the
// Dashboard sat unrouted for months before anyone noticed). Test files do
// not count as importers: a page that only its own test imports is still
// unreachable in the app.
//
// Limits: it looks one hop back (a page imported only by another dead module
// still passes), it checks the top level of src/pages only, and it does not
// read import.meta.glob calls, so routing pages through a glob would need
// this guard taught about it first.

// Every source file under src, tests included, as text, keyed '/src/…'.
// The tests are read so the guard can be seen to ignore them.
const sources = import.meta.glob(['/src/**/*.{ts,tsx}', '!/src/**/*.d.ts'], {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

const isTest = (file: string) => /\.test\.tsx?$/.test(file)

// Same aliases as vitest.config.ts / vite.config.ts / tsconfig.app.json.
const ALIASES: Record<string, string> = {
  '@/': '/src/',
  '@components/': '/src/components/',
  '@pages/': '/src/pages/',
  '@services/': '/src/services/',
  '@store/': '/src/store/',
  '@hooks/': '/src/hooks/',
  '@types/': '/src/types/',
  '@utils/': '/src/utils/',
  '@config/': '/src/config/',
}

const EXTENSIONS = ['', '.ts', '.tsx', '/index.ts', '/index.tsx']

function normalize(path: string): string {
  const out: string[] = []
  for (const part of path.split('/')) {
    if (part === '' || part === '.') continue
    if (part === '..') out.pop()
    else out.push(part)
  }
  return '/' + out.join('/')
}

function dirname(path: string): string {
  return path.slice(0, path.lastIndexOf('/'))
}

// Resolves an import specifier written in `importer` to a key of `files`,
// or null for packages and anything outside src.
function resolveImport(
  importer: string,
  specifier: string,
  files: Record<string, unknown>,
): string | null {
  const bare = specifier.split('?')[0]
  let target: string | null = null
  if (bare.startsWith('./') || bare.startsWith('../')) {
    target = normalize(`${dirname(importer)}/${bare}`)
  } else {
    const alias = Object.keys(ALIASES).find((a) => bare.startsWith(a))
    if (alias) target = normalize(ALIASES[alias] + bare.slice(alias.length))
  }
  if (target === null) return null
  for (const ext of EXTENSIONS) {
    if (target + ext in files) return target + ext
  }
  return null
}

// Every module some *other* non-test file in `files` imports (static,
// re-export or dynamic import). The TS scanner skips comments and strings.
function importedModules(files: Record<string, string>): Set<string> {
  const imported = new Set<string>()
  for (const [importer, text] of Object.entries(files)) {
    if (isTest(importer)) continue
    const { importedFiles } = ts.preProcessFile(text, true, true)
    for (const { fileName } of importedFiles) {
      const resolved = resolveImport(importer, fileName, files)
      if (resolved && resolved !== importer) imported.add(resolved)
    }
  }
  return imported
}

function orphanedPages(files: Record<string, string>): string[] {
  const imported = importedModules(files)
  return Object.keys(files)
    .filter((f) => /^\/src\/pages\/[^/]+\.tsx$/.test(f) && !isTest(f))
    .filter((f) => !imported.has(f))
    .sort()
}

describe('every page is reachable (#513)', () => {
  it('reads the source files it means to check', () => {
    // A glob that matched nothing would make the guard pass vacuously.
    expect(Object.keys(sources)).toContain('/src/App.tsx')
    expect(Object.keys(sources)).toContain('/src/pages/ConnectionsList.tsx')
    expect(Object.keys(sources)).toContain('/src/pages/ConnectionsList.test.tsx')
  })

  it('resolves the imports it has to recognise', () => {
    const files = {
      '/src/App.tsx': '',
      '/src/pages/A.tsx': '',
      '/src/pages/B.tsx': '',
      '/src/components/Nav/index.tsx': '',
      '/src/store/x.ts': '',
    }
    expect(resolveImport('/src/App.tsx', './pages/A', files)).toBe('/src/pages/A.tsx')
    expect(resolveImport('/src/pages/A.tsx', './B', files)).toBe('/src/pages/B.tsx')
    expect(resolveImport('/src/store/x.ts', '../pages/B.tsx', files)).toBe('/src/pages/B.tsx')
    expect(resolveImport('/src/store/x.ts', '@/pages/A', files)).toBe('/src/pages/A.tsx')
    expect(resolveImport('/src/store/x.ts', '@pages/B', files)).toBe('/src/pages/B.tsx')
    expect(resolveImport('/src/App.tsx', './components/Nav', files)).toBe(
      '/src/components/Nav/index.tsx',
    )
    expect(resolveImport('/src/App.tsx', './pages/A?raw', files)).toBe('/src/pages/A.tsx')
    expect(resolveImport('/src/App.tsx', 'react', files)).toBeNull()
    expect(resolveImport('/src/App.tsx', './pages/Missing', files)).toBeNull()
  })

  it('finds the real App.tsx importing ConnectionsList', () => {
    expect(importedModules(sources).has('/src/pages/ConnectionsList.tsx')).toBe(true)
  })

  it('does not count a page importing itself, or an import in a comment', () => {
    const files = {
      '/src/App.tsx': "// import Old from './pages/Old'\nimport A from './pages/A'\n",
      '/src/pages/A.tsx': '',
      '/src/pages/Old.tsx': "export * from './Old'\n",
    }
    expect(orphanedPages(files)).toEqual(['/src/pages/Old.tsx'])
  })

  it('does not count a page imported only by its own test', () => {
    const files = {
      '/src/App.tsx': "import A from './pages/A'\n",
      '/src/pages/A.tsx': '',
      '/src/pages/A.test.tsx': "import A from './A'\n",
      '/src/pages/Lonely.tsx': '',
      '/src/pages/Lonely.test.tsx': "import Lonely from './Lonely'\n",
    }
    expect(orphanedPages(files)).toEqual(['/src/pages/Lonely.tsx'])
  })

  it('counts dynamic imports and re-exports', () => {
    const files = {
      '/src/App.tsx': "const L = lazy(() => import('@/pages/Lazy'))\nexport { R } from './pages/R'\n",
      '/src/pages/Lazy.tsx': '',
      '/src/pages/R.tsx': '',
    }
    expect(orphanedPages(files)).toEqual([])
  })

  it('no page under src/pages is left without an importer', () => {
    // If this fails: route the page in App.tsx, or delete it.
    expect(orphanedPages(sources), 'pages nothing outside tests imports').toEqual([])
  })
})
