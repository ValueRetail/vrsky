import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, cleanup } from '@testing-library/react'
import { useCanvasPersistence } from './useCanvasPersistence'
import { useCanvasStore } from '../store/canvasStore'
import { useAuthStore } from '../store/authStore'
import type { Edge, Node } from '../types/pipeline'

const DEBOUNCE_MS = 2000

const node = (id: string): Node => ({
  id,
  type: 'input',
  data: { label: id },
  position: { x: 0, y: 0 },
})
const edge = (source: string, target: string): Edge => ({ id: `${source}-${target}`, source, target })

const setTenant = (id: string | null) =>
  useAuthStore.setState({ currentTenant: id ? ({ id, name: id } as never) : null })

const reset = () => {
  localStorage.clear()
  useCanvasStore.setState({ canvases: [], currentCanvasId: null, isInitialized: false, tenantId: null })
}

const stored = (tenant: string) => JSON.parse(localStorage.getItem(`vrsky:canvases:v1:${tenant}`) ?? 'null')
const storeCanvas = (id: string | null) => useCanvasStore.getState().canvases.find((c) => c.id === id)!

beforeEach(() => {
  vi.useFakeTimers()
  reset()
  setTenant('t1')
})
afterEach(() => {
  cleanup() // unmount before the stores are reset, so nothing re-renders outside act
  vi.useRealTimers()
  vi.restoreAllMocks()
  setTenant(null)
})

describe('useCanvasPersistence: loading', () => {
  it('initializes the store for the current tenant and exposes a first canvas', () => {
    const { result } = renderHook(() => useCanvasPersistence())
    expect(result.current.isInitialized).toBe(true)
    expect(result.current.canvases).toHaveLength(1)
    expect(result.current.activeCanvas?.name).toBe('Tenant 1')
    expect(result.current.currentCanvasId).toBe(result.current.activeCanvas?.id)
    expect(result.current.canCreateMore).toBe(true)
    expect(stored('t1').canvases).toHaveLength(1)
  })

  it('loads what was saved earlier for the tenant', () => {
    localStorage.setItem(
      'vrsky:canvases:v1:t1',
      JSON.stringify({
        canvases: [
          { id: 'x', name: 'First', nodes: [], edges: [], createdAt: 1, updatedAt: 1 },
          { id: 'y', name: 'Second', nodes: [node('n')], edges: [], createdAt: 2, updatedAt: 2 },
        ],
        currentCanvasId: 'y',
      })
    )
    const { result } = renderHook(() => useCanvasPersistence())
    expect(result.current.canvases.map((c) => c.name)).toEqual(['First', 'Second'])
    expect(result.current.activeCanvas?.id).toBe('y')
    expect(result.current.activeCanvas?.nodes).toEqual([node('n')])
  })

  it('falls back to the first canvas when the saved current one is gone', () => {
    localStorage.setItem(
      'vrsky:canvases:v1:t1',
      JSON.stringify({
        canvases: [{ id: 'x', name: 'First', nodes: [], edges: [], createdAt: 1, updatedAt: 1 }],
        currentCanvasId: 'deleted',
      })
    )
    const { result } = renderHook(() => useCanvasPersistence())
    expect(result.current.currentCanvasId).toBe('x')
  })

  it('starts with a default canvas when the saved data is corrupt', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    localStorage.setItem('vrsky:canvases:v1:t1', '{not json')
    const { result } = renderHook(() => useCanvasPersistence())
    expect(result.current.canvases).toHaveLength(1)
    expect(result.current.activeCanvas?.name).toBe('Tenant 1')
    expect(warn).toHaveBeenCalled()
  })

  it('switching tenant loads that tenant\'s canvases, not the previous ones', () => {
    const { result, rerender } = renderHook(() => useCanvasPersistence())
    act(() => {
      result.current.renameCanvas(result.current.currentCanvasId!, 'Mine')
    })
    act(() => setTenant('t2'))
    rerender()
    expect(result.current.activeCanvas?.name).toBe('Tenant 1')
    expect(stored('t1').canvases[0].name).toBe('Mine')
    expect(stored('t2').canvases[0].name).toBe('Tenant 1')
  })

  it('without a tenant, uses the unscoped storage key', () => {
    act(() => setTenant(null))
    renderHook(() => useCanvasPersistence())
    expect(JSON.parse(localStorage.getItem('vrsky:canvases:v1')!).canvases).toHaveLength(1)
  })
})

describe('useCanvasPersistence: debounced save', () => {
  it('writes the latest nodes and edges once, after 2 s of quiet', () => {
    const { result } = renderHook(() => useCanvasPersistence())
    const id = result.current.currentCanvasId!
    act(() => result.current.updateCanvas([node('a')], []))
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    act(() => result.current.updateCanvas([node('a'), node('b')], [edge('a', 'b')]))
    act(() => {
      vi.advanceTimersByTime(DEBOUNCE_MS - 1)
    })
    expect(storeCanvas(id).nodes).toEqual([])
    expect(stored('t1').canvases[0].nodes).toEqual([])
    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(storeCanvas(id).nodes.map((n) => n.id)).toEqual(['a', 'b'])
    expect(storeCanvas(id).edges).toEqual([edge('a', 'b')])
    expect(stored('t1').canvases[0].nodes.map((n: Node) => n.id)).toEqual(['a', 'b'])
    expect(vi.getTimerCount()).toBe(0)
  })

  it('does nothing when there is no current canvas', () => {
    const { result } = renderHook(() => useCanvasPersistence())
    act(() => useCanvasStore.setState({ currentCanvasId: null }))
    act(() => result.current.updateCanvas([node('a')], []))
    expect(vi.getTimerCount()).toBe(0)
    expect(useCanvasStore.getState().canvases[0].nodes).toEqual([])
  })

  it('a pending save is dropped when the page goes away', () => {
    const { result, unmount } = renderHook(() => useCanvasPersistence())
    act(() => result.current.updateCanvas([node('a')], []))
    unmount()
    act(() => {
      vi.advanceTimersByTime(DEBOUNCE_MS * 2)
    })
    expect(useCanvasStore.getState().canvases[0].nodes).toEqual([])
    expect(stored('t1').canvases[0].nodes).toEqual([])
  })

  it('a save that fails to reach localStorage keeps the in-memory canvas and does not throw', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { result } = renderHook(() => useCanvasPersistence())
    const id = result.current.currentCanvasId!
    vi.spyOn(localStorage, 'setItem').mockImplementation(() => {
      throw new DOMException('quota', 'QuotaExceededError')
    })
    act(() => result.current.updateCanvas([node('a')], []))
    expect(() =>
      act(() => {
        vi.advanceTimersByTime(DEBOUNCE_MS)
      })
    ).not.toThrow()
    expect(storeCanvas(id).nodes.map((n) => n.id)).toEqual(['a'])
    expect(result.current.activeCanvas?.nodes.map((n) => n.id)).toEqual(['a'])
    expect(warn).toHaveBeenCalledWith('Failed to save canvases to localStorage:', expect.anything())
    // nothing new was persisted
    expect(stored('t1').canvases[0].nodes).toEqual([])
  })

  it('a failed save does not stop the next one from landing', () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { result } = renderHook(() => useCanvasPersistence())
    const setItem = vi.spyOn(localStorage, 'setItem').mockImplementationOnce(() => {
      throw new DOMException('quota', 'QuotaExceededError')
    })
    act(() => result.current.forceUpdateCanvas([node('a')], []))
    expect(stored('t1').canvases[0].nodes).toEqual([])
    act(() => result.current.forceUpdateCanvas([node('a'), node('b')], []))
    expect(setItem).toHaveBeenCalledTimes(2)
    expect(stored('t1').canvases[0].nodes.map((n: Node) => n.id)).toEqual(['a', 'b'])
  })
})

describe('useCanvasPersistence: forced save', () => {
  it('writes at once and cancels the pending debounced save', () => {
    const { result } = renderHook(() => useCanvasPersistence())
    const id = result.current.currentCanvasId!
    act(() => result.current.updateCanvas([node('old')], []))
    act(() => result.current.forceUpdateCanvas([node('new')], [edge('new', 'new2')]))
    expect(storeCanvas(id).nodes.map((n) => n.id)).toEqual(['new'])
    expect(stored('t1').canvases[0].edges).toEqual([edge('new', 'new2')])
    expect(vi.getTimerCount()).toBe(0)
    act(() => {
      vi.advanceTimersByTime(DEBOUNCE_MS * 2)
    })
    expect(storeCanvas(id).nodes.map((n) => n.id)).toEqual(['new'])
  })

  it('works with nothing pending', () => {
    const { result } = renderHook(() => useCanvasPersistence())
    const id = result.current.currentCanvasId!
    act(() => result.current.forceUpdateCanvas([node('a')], []))
    expect(storeCanvas(id).nodes.map((n) => n.id)).toEqual(['a'])
  })

  it('does nothing when there is no current canvas', () => {
    const { result } = renderHook(() => useCanvasPersistence())
    act(() => useCanvasStore.setState({ currentCanvasId: null }))
    act(() => result.current.forceUpdateCanvas([node('a')], []))
    expect(useCanvasStore.getState().canvases[0].nodes).toEqual([])
  })
})

describe('useCanvasPersistence: canvas actions', () => {
  it('create, rename, switch and delete go through to the store and storage', () => {
    const { result } = renderHook(() => useCanvasPersistence())
    const first = result.current.currentCanvasId!
    let created: ReturnType<typeof result.current.createCanvas> = null
    act(() => {
      created = result.current.createCanvas()
    })
    expect(created!.name).toBe('Tenant 2')
    expect(result.current.currentCanvasId).toBe(created!.id)

    act(() => result.current.renameCanvas(created!.id, '  Orders  '))
    expect(result.current.canvases.find((c) => c.id === created!.id)?.name).toBe('Orders')

    act(() => result.current.switchCanvas(first))
    expect(result.current.currentCanvasId).toBe(first)
    expect(stored('t1').currentCanvasId).toBe(first)

    act(() => result.current.deleteCanvas(created!.id))
    expect(result.current.canvases.map((c) => c.id)).toEqual([first])
    expect(stored('t1').canvases).toHaveLength(1)
  })

  it('an update after a switch is saved to the canvas that is now current', () => {
    const { result } = renderHook(() => useCanvasPersistence())
    const first = result.current.currentCanvasId!
    let second = ''
    act(() => {
      second = result.current.createCanvas()!.id
    })
    act(() => result.current.switchCanvas(first))
    act(() => result.current.forceUpdateCanvas([node('a')], []))
    expect(storeCanvas(first).nodes.map((n) => n.id)).toEqual(['a'])
    expect(storeCanvas(second).nodes).toEqual([])
  })

  it('import adds a linked canvas, and the deployed link can be set and cleared', () => {
    const { result } = renderHook(() => useCanvasPersistence())
    let imported: ReturnType<typeof result.current.importCanvas> = null
    act(() => {
      imported = result.current.importCanvas('Edit: x', [node('a')], [], 'conn-1', 'x')
    })
    expect(imported!.deployedConnectionId).toBe('conn-1')
    expect(result.current.activeCanvas?.nodes).toHaveLength(1)

    act(() => result.current.setDeployedConnectionId(imported!.id, null))
    expect(result.current.activeCanvas?.deployedConnectionId).toBeUndefined()
  })

  it('createCanvas returns null at the limit and canCreateMore turns false', () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { result } = renderHook(() => useCanvasPersistence())
    act(() => {
      for (let i = 0; i < 9; i++) result.current.createCanvas()
    })
    expect(result.current.canvases).toHaveLength(10)
    expect(result.current.canCreateMore).toBe(false)
    let extra: unknown = 'unset'
    act(() => {
      extra = result.current.createCanvas()
    })
    expect(extra).toBeNull()
    expect(result.current.canvases).toHaveLength(10)
  })
})
