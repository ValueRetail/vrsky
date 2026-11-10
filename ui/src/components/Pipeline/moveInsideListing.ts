// moveInsideListing mirrors objectstore.MoveInsideListing (Go,
// src/pkg/objectstore/moveprefix.go); change both together: true when a
// move into movePrefix lands on keys that a listing of prefix selects again.
// The providers match prefix as text, not as a folder, so "in" also lists
// "in-done/a.csv". Leading slashes are ignored, which refuses more, not less.
export function moveInsideListing(prefix: string, movePrefix: string): boolean {
  if (!movePrefix) return false
  const p = prefix.replace(/^\/+/, '')
  // Go's path.Clean: ".." removes the segment before it, is dropped at the
  // root of a rooted path, and is kept at the start of a relative one.
  const rooted = movePrefix.startsWith('/')
  const parts: string[] = []
  for (const seg of movePrefix.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg !== '..') parts.push(seg)
    else if (parts.length && parts[parts.length - 1] !== '..') parts.pop()
    else if (!rooted) parts.push('..')
  }
  const dir = parts.length ? parts.join('/') + '/' : ''
  if (dir.startsWith(p)) return true
  if (!p.startsWith(dir)) return false
  return !p.slice(dir.length).includes('/')
}
