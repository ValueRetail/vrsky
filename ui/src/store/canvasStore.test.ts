/**
 * The canvas store remembers which connection a canvas deployed and the name
 * the server has for it, across a reload (plans/builder-deploy-names.md): a
 * deploy compares the tab name with that name to tell a rename from no change.
 */

import { describe, it, expect, beforeEach } from 'vitest'
import { useCanvasStore } from './canvasStore'

const reload = () => {
  useCanvasStore.setState({ canvases: [], currentCanvasId: null, isInitialized: false, tenantId: null })
  useCanvasStore.getState().initialize('t1')
  return useCanvasStore.getState()
}

beforeEach(() => {
  localStorage.clear()
  reload()
})

describe('canvasStore: the deployed connection and its name', () => {
  it('an imported edit canvas keeps the server name, and it survives a reload', () => {
    const c = useCanvasStore.getState().importCanvas('Edit: BC → Bifrost', [], [], 'conn-1', 'BC → Bifrost')!
    const after = reload().canvases.find((x) => x.id === c.id)!
    expect(after.deployedConnectionId).toBe('conn-1')
    expect(after.deployedName).toBe('BC → Bifrost')
  })

  it('a deploy that renamed records the new name; one that sent none keeps the old', () => {
    const c = useCanvasStore.getState().importCanvas('Edit: a', [], [], 'conn-1', 'a')!
    useCanvasStore.getState().setDeployedConnectionId(c.id, 'conn-1', 'b')
    expect(reload().canvases.find((x) => x.id === c.id)!.deployedName).toBe('b')
    useCanvasStore.getState().setDeployedConnectionId(c.id, 'conn-1')
    expect(reload().canvases.find((x) => x.id === c.id)!.deployedName).toBe('b')
  })

  it('unlinking drops the name with the connection', () => {
    const c = useCanvasStore.getState().importCanvas('Edit: a', [], [], 'conn-1', 'a')!
    useCanvasStore.getState().setDeployedConnectionId(c.id, null)
    const after = reload().canvases.find((x) => x.id === c.id)!
    expect(after.deployedConnectionId).toBeUndefined()
    expect(after.deployedName).toBeUndefined()
  })
})

// The rest of the store: it holds the user's canvases, up to ten per
// workspace, in localStorage keyed by tenant.
describe('canvasStore: canvases', () => {
  const s = () => useCanvasStore.getState()

  it('a fresh workspace gets one canvas, "Tenant 1", and it is current', () => {
    expect(s().canvases.map((c) => c.name)).toEqual(['Tenant 1'])
    expect(s().getActiveCanvas()?.name).toBe('Tenant 1')
  })

  it('new canvases take the next free "Tenant N" and become current', () => {
    const b = s().createCanvas()!
    expect(b.name).toBe('Tenant 2')
    expect(s().currentCanvasId).toBe(b.id)
    expect(s().createCanvas('Orders')!.name).toBe('Orders')
  })

  it('stops at ten canvases, for create and import alike', () => {
    for (let i = 0; i < 9; i++) s().createCanvas()
    expect(s().canCreateMore()).toBe(false)
    expect(s().createCanvas()).toBeNull()
    expect(s().importCanvas('x', [], [], 'c')).toBeNull()
    expect(s().canvases).toHaveLength(10)
  })

  it('updates, renames (trimmed, never empty) and survives a reload', () => {
    const id = s().currentCanvasId!
    const node = { id: 'n1', type: 'input', position: { x: 0, y: 0 }, data: {} } as never
    s().updateCanvas(id, [node], [])
    s().renameCanvas(id, '  Bifrost  ')
    s().renameCanvas(id, '   ')
    const c = reload().getCanvasById(id)!
    expect(c.name).toBe('Bifrost')
    expect(c.nodes).toHaveLength(1)
  })

  it('switches only to a canvas that exists', () => {
    const first = s().currentCanvasId!
    const b = s().createCanvas()!
    s().switchCanvas(first)
    expect(s().currentCanvasId).toBe(first)
    s().switchCanvas('nope')
    expect(s().currentCanvasId).toBe(first)
    expect(reload().currentCanvasId).toBe(first)
    expect(s().getCanvasById(b.id)).toBeDefined()
  })

  it('deleting the current canvas moves to the first left; the last one stays', () => {
    const first = s().currentCanvasId!
    const b = s().createCanvas()!
    s().deleteCanvas(b.id)
    expect(s().currentCanvasId).toBe(first)
    s().deleteCanvas(first)
    expect(s().canvases).toHaveLength(1)
  })

  it('a stored current id that no longer exists falls back to the first canvas', () => {
    const first = s().currentCanvasId!
    localStorage.setItem('vrsky:canvases:v1:t1', JSON.stringify({ canvases: s().canvases, currentCanvasId: 'gone' }))
    expect(reload().currentCanvasId).toBe(first)
  })

  it('each workspace keeps its own canvases', () => {
    s().renameCanvas(s().currentCanvasId!, 'Mine')
    useCanvasStore.getState().initialize('t2')
    expect(s().canvases.map((c) => c.name)).toEqual(['Tenant 1'])
    expect(reload().canvases.map((c) => c.name)).toEqual(['Mine'])
  })
})

// #502: opening a linked connection fetches its name; the answer brings the
// canvas's deployedName (and an unrenamed tab) in line with the server.
describe('canvasStore: syncDeployedName', () => {
  const s = () => useCanvasStore.getState()
  const stored = (id: string) => reload().canvases.find((x) => x.id === id)!
  const STORAGE_T1 = 'vrsky:canvases:v1:t1'

  it('updates the linked canvas and persists it', () => {
    const c = s().importCanvas('Edit: Old name', [], [], 'conn-1', 'Old name')!
    s().syncDeployedName(c.id, 'conn-1', 'New name')
    expect(s().getCanvasById(c.id)).toMatchObject({ name: 'Edit: New name', deployedName: 'New name', deployedConnectionId: 'conn-1' })
    expect(stored(c.id)).toMatchObject({ name: 'Edit: New name', deployedName: 'New name', deployedConnectionId: 'conn-1' })
  })

  it('a tab renamed here keeps its name; deployedName still follows the server', () => {
    const c = s().importCanvas('Edit: Old name', [], [], 'conn-1', 'Old name')!
    s().renameCanvas(c.id, 'Mine')
    s().syncDeployedName(c.id, 'conn-1', 'New name')
    expect(stored(c.id)).toMatchObject({ name: 'Mine', deployedName: 'New name' })
  })

  it('a rename made after the request went out wins over the answer', () => {
    const c = s().importCanvas('Edit: Old name', [], [], 'conn-1', 'Old name')!
    // The request goes out with the tab reading "Edit: Old name"; the user
    // renames before it answers.
    s().renameCanvas(c.id, 'Typed meanwhile')
    s().syncDeployedName(c.id, 'conn-1', 'New name')
    expect(stored(c.id)).toMatchObject({ name: 'Typed meanwhile', deployedName: 'New name' })
  })

  it('keeps nodes, edges, the current canvas and the other canvases', () => {
    const node = { id: 'n1', type: 'input', position: { x: 10, y: 20 }, data: { label: 'In' } } as const
    const c = s().importCanvas('Edit: Old name', [node], [{ source: 'n1', target: 'n1' }], 'conn-1', 'Old name')!
    const first = s().canvases[0].id
    s().switchCanvas(first)
    const others = s().canvases.filter((x) => x.id !== c.id)
    s().syncDeployedName(c.id, 'conn-1', 'New name')
    const after = reload()
    const synced = after.canvases.find((x) => x.id === c.id)!
    expect(synced.nodes).toEqual([node])
    expect(synced.edges).toEqual([{ source: 'n1', target: 'n1' }])
    expect(after.currentCanvasId).toBe(first)
    expect(after.canvases.filter((x) => x.id !== c.id)).toEqual(others)
  })

  it('trims the server name', () => {
    const c = s().importCanvas('Edit: Old name', [], [], 'conn-1', 'Old name')!
    s().syncDeployedName(c.id, 'conn-1', '  New name  ')
    expect(stored(c.id)).toMatchObject({ name: 'Edit: New name', deployedName: 'New name' })
  })

  it.each([
    ['a canvas linked to a different connection', (id: string) => s().syncDeployedName(id, 'conn-2', 'New name')],
    ['an unknown canvas id', () => s().syncDeployedName('nope', 'conn-1', 'New name')],
    ['a blank server name', (id: string) => s().syncDeployedName(id, 'conn-1', '   ')],
    ['an empty server name', (id: string) => s().syncDeployedName(id, 'conn-1', '')],
  ])('%s changes nothing', (_, act) => {
    const c = s().importCanvas('Edit: Old name', [], [], 'conn-1', 'Old name')!
    const before = s().canvases
    const storedBefore = localStorage.getItem(STORAGE_T1)
    act(c.id)
    expect(s().canvases).toEqual(before)
    expect(s().getCanvasById(c.id)).toMatchObject({ name: 'Edit: Old name', deployedName: 'Old name' })
    expect(localStorage.getItem(STORAGE_T1)).toBe(storedBefore)
  })

  it('a canvas unlinked while the request was out stays unlinked and keeps its name', () => {
    const c = s().importCanvas('Edit: Old name', [], [], 'conn-1', 'Old name')!
    s().setDeployedConnectionId(c.id, null)
    s().syncDeployedName(c.id, 'conn-1', 'New name')
    const after = stored(c.id)
    expect(after.name).toBe('Edit: Old name')
    expect(after.deployedConnectionId).toBeUndefined()
    expect(after.deployedName).toBeUndefined()
  })

  it('a canvas from another workspace is not touched after a switch', () => {
    const c = s().importCanvas('Edit: Old name', [], [], 'conn-1', 'Old name')!
    s().initialize('t2')
    s().syncDeployedName(c.id, 'conn-1', 'New name')
    expect(s().canvases.some((x) => x.id === c.id)).toBe(false)
    expect(localStorage.getItem('vrsky:canvases:v1:t2')).not.toContain('New name')
    expect(stored(c.id)).toMatchObject({ name: 'Edit: Old name', deployedName: 'Old name' })
  })
})
