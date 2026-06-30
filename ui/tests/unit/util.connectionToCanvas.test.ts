import { describe, it, expect } from 'vitest'
import { connectionToCanvas } from '@/utils/connectionToCanvas'
import type { Connection } from '@/types/models'

const baseConn = (overrides: Partial<Connection>): Connection =>
  ({
    id: 'conn-1',
    name: 'Test',
    status: 'stopped',
    created_at: '2024-01-01T00:00:00Z',
    updated_at: '2024-01-01T00:00:00Z',
    ...overrides,
  }) as Connection

describe('connectionToCanvas', () => {
  it('maps API node types back to canvas node types', () => {
    const conn = baseConn({
      nodes: [
        { id: 'a', type: 'consumer', config: { type: 'http' } },
        { id: 'b', type: 'filter', config: {} },
        { id: 'c', type: 'converter', config: {} },
        { id: 'd', type: 'producer', config: { type: 'file' } },
      ],
      edges: [
        { source: 'a', target: 'b' },
        { source: 'b', target: 'c' },
        { source: 'c', target: 'd' },
      ],
    })
    const { nodes } = connectionToCanvas(conn)
    expect(nodes.map((n) => n.type)).toEqual(['input', 'filter', 'converter', 'output'])
  })

  it('carries node config through and labels by connector type', () => {
    const conn = baseConn({
      nodes: [{ id: 'a', type: 'consumer', config: { type: 'http', url: '/hook' } }],
      edges: [],
    })
    const { nodes } = connectionToCanvas(conn)
    expect(nodes[0].data.config).toEqual({ type: 'http', url: '/hook' })
    expect(nodes[0].data.label).toBe('http')
  })

  it('lays out nodes left-to-right by graph depth', () => {
    const conn = baseConn({
      nodes: [
        { id: 'a', type: 'consumer', config: {} },
        { id: 'b', type: 'filter', config: {} },
        { id: 'd', type: 'producer', config: {} },
      ],
      edges: [
        { source: 'a', target: 'b' },
        { source: 'b', target: 'd' },
      ],
    })
    const { nodes } = connectionToCanvas(conn)
    const x = Object.fromEntries(nodes.map((n) => [n.id, n.position.x]))
    expect(x.a).toBeLessThan(x.b)
    expect(x.b).toBeLessThan(x.d)
  })

  it('preserves edges with stable ids', () => {
    const conn = baseConn({
      nodes: [
        { id: 'a', type: 'consumer', config: {} },
        { id: 'b', type: 'producer', config: {} },
      ],
      edges: [{ source: 'a', target: 'b' }],
    })
    const { edges } = connectionToCanvas(conn)
    expect(edges).toHaveLength(1)
    expect(edges[0]).toMatchObject({ source: 'a', target: 'b' })
    expect(edges[0].id).toBeTruthy()
  })

  it('handles an empty / graph-less connection without throwing', () => {
    const { nodes, edges } = connectionToCanvas(baseConn({}))
    expect(nodes).toEqual([])
    expect(edges).toEqual([])
  })
})
