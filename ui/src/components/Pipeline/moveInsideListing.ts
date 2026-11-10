// moveInsideListing mirrors objectstore.MoveInsideListing (Go): true when a
// move into movePrefix lands on keys that a listing of prefix selects again.
// The providers match prefix as text, not as a folder, so "in" also lists
// "in-done/a.csv". Leading slashes are ignored, which refuses more, not less.
export function moveInsideListing(prefix: string, movePrefix: string): boolean {
  if (!movePrefix) return false
  const p = prefix.replace(/^\/+/, '')
  const parts: string[] = []
  for (const seg of movePrefix.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') parts.pop()
    else parts.push(seg)
  }
  const dir = parts.length ? parts.join('/') + '/' : ''
  if (dir.startsWith(p)) return true
  if (!p.startsWith(dir)) return false
  return !p.slice(dir.length).includes('/')
}
