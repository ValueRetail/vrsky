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
