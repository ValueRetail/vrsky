import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import type Konva from 'konva'
import { useConnectionDrawing } from './useConnectionDrawing'
import type { Edge, Node } from '../types/pipeline'

const nodes: Node[] = ['a', 'b', 'c'].map((id, i) => ({
  id,
  type: 'filter',
  data: { label: id },
  position: { x: i * 100, y: 0 },
}))

const konvaEvent = () => ({ cancelBubble: false }) as unknown as Konva.KonvaEventObject<MouseEvent>

const setup = (edges: Edge[] = []) => {
  const setEdges = vi.fn()
  const hook = renderHook(({ e }) => useConnectionDrawing(nodes, setEdges, e), {
    initialProps: { e: edges },
  })
  return { setEdges, ...hook }
}

const startFrom = (r: ReturnType<typeof setup>['result'], id = 'a') =>
  act(() => r.current.handlePortMouseDown(id, 'output', konvaEvent()))

const idle = {
  connectionDrawing: false,
  connectionStart: null,
  connectionPreviewEnd: null,
}
const state = (r: ReturnType<typeof setup>['result']) => ({
  connectionDrawing: r.current.connectionDrawing,
  connectionStart: r.current.connectionStart,
  connectionPreviewEnd: r.current.connectionPreviewEnd,
})

beforeEach(() => {
  vi.useFakeTimers()
})
afterEach(() => {
  vi.useRealTimers()
})

describe('useConnectionDrawing: starting', () => {
  it('starts idle', () => {
    const { result } = setup()
    expect(state(result)).toEqual(idle)
  })

  it('mouse down on an output port starts a drawing from that node', () => {
    const { result } = setup()
    const ev = konvaEvent()
    act(() => result.current.handlePortMouseDown('a', 'output', ev))
    expect(result.current.connectionDrawing).toBe(true)
    expect(result.current.connectionStart).toEqual({ nodeId: 'a', port: 'output' })
    expect(ev.cancelBubble).toBe(true)
  })

  it('mouse down on an input port starts nothing, but still stops the event bubbling', () => {
    const { result } = setup()
    const ev = konvaEvent()
    act(() => result.current.handlePortMouseDown('a', 'input', ev))
    expect(state(result)).toEqual(idle)
    expect(ev.cancelBubble).toBe(true)
  })
})

describe('useConnectionDrawing: preview line', () => {
  it('follows the pointer while drawing', () => {
    const { result } = setup()
    startFrom(result)
    act(() => result.current.handleStageMouseMove(10, 20))
    act(() => result.current.handleStageMouseMove(30, 40))
    expect(result.current.connectionPreviewEnd).toEqual({ x: 30, y: 40 })
  })

  it('is ignored while nothing is being drawn', () => {
    const { result } = setup()
    act(() => result.current.handleStageMouseMove(10, 20))
    expect(result.current.connectionPreviewEnd).toBeNull()
  })

  it('can be set directly', () => {
    const { result } = setup()
    act(() => result.current.setConnectionPreviewEnd({ x: 1, y: 2 }))
    expect(result.current.connectionPreviewEnd).toEqual({ x: 1, y: 2 })
  })
})

describe('useConnectionDrawing: completing', () => {
  it('dropping on another node input adds one edge after the existing ones and resets', () => {
    const existing: Edge[] = [{ id: 'b-c', source: 'b', target: 'c' }]
    const { result, setEdges } = setup(existing)
    startFrom(result, 'a')
    act(() => result.current.handleStageMouseMove(5, 5))
    act(() => result.current.handlePortMouseUp('b', 'input'))
    expect(setEdges).toHaveBeenCalledTimes(1)
    expect(setEdges).toHaveBeenCalledWith([...existing, { id: 'a-b', source: 'a', target: 'b' }])
    expect(state(result)).toEqual(idle)
  })

  it('the same edge twice is not added again, and the drawing still ends', () => {
    const { result, setEdges } = setup([{ id: 'a-b', source: 'a', target: 'b' }])
    startFrom(result, 'a')
    act(() => result.current.handlePortMouseUp('b', 'input'))
    expect(setEdges).not.toHaveBeenCalled()
    expect(state(result)).toEqual(idle)
  })

  it('the opposite direction is a different edge', () => {
    const { result, setEdges } = setup([{ id: 'a-b', source: 'a', target: 'b' }])
    startFrom(result, 'b')
    act(() => result.current.handlePortMouseUp('a', 'input'))
    expect(setEdges).toHaveBeenCalledWith([
      { id: 'a-b', source: 'a', target: 'b' },
      { id: 'b-a', source: 'b', target: 'a' },
    ])
  })

  it('a node cannot connect to itself; the drawing ends with no edge', () => {
    const { result, setEdges } = setup()
    startFrom(result, 'a')
    act(() => result.current.handleStageMouseMove(5, 5))
    act(() => result.current.handlePortMouseUp('a', 'input'))
    expect(setEdges).not.toHaveBeenCalled()
    expect(state(result)).toEqual(idle)
  })

  it('dropping on an output port makes no edge and keeps the drawing going', () => {
    const { result, setEdges } = setup()
    startFrom(result, 'a')
    act(() => result.current.handlePortMouseUp('b', 'output'))
    expect(setEdges).not.toHaveBeenCalled()
    expect(result.current.connectionDrawing).toBe(true)
    expect(result.current.connectionStart).toEqual({ nodeId: 'a', port: 'output' })
  })

  it('mouse up on a port with no drawing in progress does nothing', () => {
    const { result, setEdges } = setup()
    act(() => result.current.handlePortMouseUp('b', 'input'))
    expect(setEdges).not.toHaveBeenCalled()
    expect(state(result)).toEqual(idle)
  })

  it('uses the edges of the latest render when it adds one', () => {
    const { result, rerender, setEdges } = setup([])
    startFrom(result, 'a')
    const later: Edge[] = [{ id: 'b-c', source: 'b', target: 'c' }]
    rerender({ e: later })
    act(() => result.current.handlePortMouseUp('c', 'input'))
    expect(setEdges).toHaveBeenCalledWith([...later, { id: 'a-c', source: 'a', target: 'c' }])
  })
})

describe('useConnectionDrawing: cancelling', () => {
  it('cancelConnectionDrawing resets everything and makes no edge', () => {
    const { result, setEdges } = setup()
    startFrom(result)
    act(() => result.current.handleStageMouseMove(5, 5))
    act(() => result.current.cancelConnectionDrawing())
    expect(state(result)).toEqual(idle)
    expect(setEdges).not.toHaveBeenCalled()
  })

  it('cancelConnectionDrawing while idle leaves a preview point alone', () => {
    const { result } = setup()
    act(() => result.current.setConnectionPreviewEnd({ x: 1, y: 2 }))
    act(() => result.current.cancelConnectionDrawing())
    expect(result.current.connectionPreviewEnd).toEqual({ x: 1, y: 2 })
  })

  it('Escape cancels a drawing', () => {
    const { result, setEdges } = setup()
    startFrom(result)
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    })
    expect(state(result)).toEqual(idle)
    expect(setEdges).not.toHaveBeenCalled()
  })

  it('other keys do not', () => {
    const { result } = setup()
    startFrom(result)
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    })
    expect(result.current.connectionDrawing).toBe(true)
  })

  it('Escape after unmount touches nothing', () => {
    const remove = vi.spyOn(window, 'removeEventListener')
    try {
      const { result, unmount } = setup()
      startFrom(result)
      unmount()
      expect(remove.mock.calls.some(([type]) => type === 'keydown')).toBe(true)
      expect(() =>
        act(() => {
          window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
        })
      ).not.toThrow()
    } finally {
      remove.mockRestore()
    }
  })

  it('a mouse up that lands off every port cancels after a short delay, with no edge', () => {
    const { result, setEdges } = setup()
    startFrom(result)
    act(() => {
      window.dispatchEvent(new MouseEvent('mouseup'))
    })
    // not yet: a port's own mouse up gets the first go
    expect(result.current.connectionDrawing).toBe(true)
    act(() => {
      vi.advanceTimersByTime(10)
    })
    expect(state(result)).toEqual(idle)
    expect(setEdges).not.toHaveBeenCalled()
  })

  it('a port drop that wins the race still produces its edge', () => {
    const { result, setEdges } = setup()
    startFrom(result, 'a')
    act(() => {
      result.current.handlePortMouseUp('b', 'input')
      window.dispatchEvent(new MouseEvent('mouseup'))
      vi.advanceTimersByTime(10)
    })
    expect(setEdges).toHaveBeenCalledTimes(1)
    expect(setEdges).toHaveBeenCalledWith([{ id: 'a-b', source: 'a', target: 'b' }])
    expect(state(result)).toEqual(idle)
  })

  it('a release over an output port ends the drawing with no edge', () => {
    const { result, setEdges } = setup()
    startFrom(result, 'a')
    act(() => {
      result.current.handlePortMouseUp('b', 'output')
      window.dispatchEvent(new MouseEvent('mouseup'))
      vi.advanceTimersByTime(10)
    })
    expect(setEdges).not.toHaveBeenCalled()
    expect(state(result)).toEqual(idle)
  })

  it('mouse up while idle schedules nothing', () => {
    const { result } = setup()
    act(() => {
      window.dispatchEvent(new MouseEvent('mouseup'))
    })
    expect(vi.getTimerCount()).toBe(0)
    act(() => {
      vi.advanceTimersByTime(50)
    })
    expect(vi.getTimerCount()).toBe(0)
    expect(state(result)).toEqual(idle)
  })

  it('stops listening for mouse up once the drawing has ended', () => {
    const remove = vi.spyOn(window, 'removeEventListener')
    try {
      const { result } = setup()
      startFrom(result)
      act(() => result.current.cancelConnectionDrawing())
      expect(remove.mock.calls.some(([type]) => type === 'mouseup')).toBe(true)
    } finally {
      remove.mockRestore()
    }
  })
})
