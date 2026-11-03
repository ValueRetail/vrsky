import { describe, it, expect, vi } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useNodeDrag } from './useNodeDrag'
import type { Node } from '../types/pipeline'

const node = (id: string, x: number, y: number): Node => ({
  id,
  type: 'filter',
  data: { label: id },
  position: { x, y },
})

const setup = (nodes: Node[]) => {
  const setNodes = vi.fn()
  const hook = renderHook(({ n }) => useNodeDrag(n, setNodes), { initialProps: { n: nodes } })
  return { setNodes, ...hook }
}

describe('useNodeDrag', () => {
  it.each([
    [0, 0, 0, 0],
    [20, 40, 20, 40],
    [29, 31, 20, 40],
    [31, 49, 40, 40],
    [10, 90, 20, 100],
    [-30, -50, -20, -40],
  ])('drag to (%d, %d) lands on grid point (%d, %d)', (x, y, wantX, wantY) => {
    const { result, setNodes } = setup([node('a', 100, 100)])
    result.current.handleNodeDrag('a', x, y)
    expect(setNodes).toHaveBeenCalledTimes(1)
    expect(setNodes.mock.calls[0][0][0].position).toEqual({ x: wantX, y: wantY })
  })

  it('moves only the dragged node and keeps the rest of it intact', () => {
    const a = { ...node('a', 0, 0), data: { label: 'A', config: { k: 1 } } }
    const b = node('b', 60, 60)
    const { result, setNodes } = setup([a, b])
    result.current.handleNodeDrag('a', 200, 120)
    const [next] = setNodes.mock.calls[0]
    expect(next).toHaveLength(2)
    expect(next[0]).toEqual({ ...a, position: { x: 200, y: 120 } })
    expect(next[1]).toBe(b)
  })

  it('does not mutate the nodes it was given', () => {
    const a = node('a', 0, 0)
    const { result } = setup([a])
    result.current.handleNodeDrag('a', 100, 100)
    expect(a.position).toEqual({ x: 0, y: 0 })
  })

  it('an unknown id leaves every position as it was', () => {
    const a = node('a', 20, 20)
    const b = node('b', 40, 40)
    const { result, setNodes } = setup([a, b])
    result.current.handleNodeDrag('ghost', 500, 500)
    expect(setNodes.mock.calls[0][0].map((n: Node) => n.position)).toEqual([
      { x: 20, y: 20 },
      { x: 40, y: 40 },
    ])
  })

  it('drags the nodes of the latest render, not the first', () => {
    const { result, rerender, setNodes } = setup([node('a', 0, 0)])
    rerender({ n: [node('a', 0, 0), node('b', 80, 80)] })
    result.current.handleNodeDrag('b', 120, 140)
    const next = setNodes.mock.calls[0][0] as Node[]
    expect(next.map((n) => n.id)).toEqual(['a', 'b'])
    expect(next[1].position).toEqual({ x: 120, y: 140 })
  })

  it('keeps the same handler while nodes and setter stay the same', () => {
    const nodes = [node('a', 0, 0)]
    const setNodes = vi.fn()
    const { result, rerender } = renderHook(() => useNodeDrag(nodes, setNodes))
    const first = result.current.handleNodeDrag
    rerender()
    expect(result.current.handleNodeDrag).toBe(first)
  })
})
