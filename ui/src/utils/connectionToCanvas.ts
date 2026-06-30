/**
 * connectionToCanvas
 *
 * Rebuilds builder-canvas nodes/edges from a saved connection's graph model
 * (the API stores {id, type: consumer|filter|converter|producer, config} with
 * NO canvas positions — see buildConnectionPayload in PipelineBuilder). Used by
 * the Edit flow (#128) when no local canvas is linked to the connection, e.g.
 * it was created in another browser or localStorage was cleared.
 *
 * Positions are reconstructed with a simple layered (left→right) layout:
 * x by longest-path depth from a source node, y staggered within each layer.
 */

import type { Connection, ConnectionNode, ConnectionEdge } from '../types/models'
import type { Node, Edge } from '../types/pipeline'

const COL_WIDTH = 260
const ROW_HEIGHT = 140
const ORIGIN = { x: 120, y: 120 }

const API_TO_CANVAS_TYPE: Record<string, Node['type']> = {
  consumer: 'input',
  producer: 'output',
  filter: 'filter',
  converter: 'converter',
}

// Longest-path depth per node (its column). Sources (no incoming edge) sit at
// depth 0; each edge pushes its target at least one column to the right. Robust
// to branches; cycles can't occur (the builder validates against them).
function computeDepths(nodes: ConnectionNode[], edges: ConnectionEdge[]): Map<string, number> {
  const adj = new Map<string, string[]>()
  const indeg = new Map<string, number>()
  nodes.forEach((n) => indeg.set(n.id, 0))
  edges.forEach((e) => {
    adj.set(e.source, [...(adj.get(e.source) ?? []), e.target])
    indeg.set(e.target, (indeg.get(e.target) ?? 0) + 1)
  })

  const depth = new Map<string, number>()
  const queue = nodes.filter((n) => (indeg.get(n.id) ?? 0) === 0).map((n) => n.id)
  queue.forEach((id) => depth.set(id, 0))

  // Kahn's algorithm, relaxing depth to the longest path seen so far.
  const remaining = new Map(indeg)
  while (queue.length) {
    const id = queue.shift() as string
    const d = depth.get(id) ?? 0
    for (const next of adj.get(id) ?? []) {
      depth.set(next, Math.max(depth.get(next) ?? 0, d + 1))
      const left = (remaining.get(next) ?? 0) - 1
      remaining.set(next, left)
      if (left === 0) queue.push(next)
    }
  }
  // Any node never reached (disconnected) defaults to depth 0.
  nodes.forEach((n) => { if (!depth.has(n.id)) depth.set(n.id, 0) })
  return depth
}

export function connectionToCanvas(connection: Connection): { nodes: Node[]; edges: Edge[] } {
  const apiNodes: ConnectionNode[] = connection.nodes ?? []
  const apiEdges: ConnectionEdge[] = connection.edges ?? []

  const depth = computeDepths(apiNodes, apiEdges)
  const perColumnCount = new Map<number, number>()

  const nodes: Node[] = apiNodes.map((n) => {
    const type = API_TO_CANVAS_TYPE[n.type] ?? 'filter'
    const config = (n.config ?? {}) as Record<string, unknown>
    const connector = typeof config.type === 'string' ? config.type : n.type
    const col = depth.get(n.id) ?? 0
    const row = perColumnCount.get(col) ?? 0
    perColumnCount.set(col, row + 1)
    return {
      id: n.id,
      type,
      data: {
        label: connector,
        type,
        config,
      },
      position: {
        x: ORIGIN.x + col * COL_WIDTH,
        y: ORIGIN.y + row * ROW_HEIGHT,
      },
    }
  })

  const edges: Edge[] = apiEdges.map((e, index) => ({
    id: e.id || `edge-${index}`,
    source: e.source,
    target: e.target,
  }))

  return { nodes, edges }
}
