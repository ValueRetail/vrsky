/**
 * Comprehensive tests for Pipeline Validation Utilities
 *
 * Test coverage:
 * - Valid pipelines (simple and complex)
 * - Consumer/producer count validation
 * - Edge reference validation
 * - Cycle detection
 * - Reachability checks
 * - Orphaned node detection
 * - Error message clarity
 */

import { describe, it, expect } from 'vitest'
import type { Node, Edge } from '../types/pipeline'
import {
  validatePipelineConnections,
  hasOutgoingEdge,
  hasIncomingEdge,
  isReachable,
  hasCycle,
  findCycle,
  findOrphanedNodes,
  getNodesOnPath,
  buildAdjacencyList,
  buildReverseAdjacencyList,
  getReachableNodes,
  getNodesReachingTarget,
} from './validation'

// =============================================================================
// Test Helpers
// =============================================================================

function createNode(
  id: string,
  type: 'consumer' | 'filter' | 'converter' | 'producer',
  label?: string
): Node {
  return {
    id,
    type,
    data: {
      label: label || `${type}-${id}`,
      config: {},
    },
    position: { x: 0, y: 0 },
  }
}

function createEdge(source: string, target: string, id?: string): Edge {
  return {
    id: id || `edge-${source}-${target}`,
    source,
    target,
  }
}

// =============================================================================
// Helper Function Tests
// =============================================================================

describe('hasOutgoingEdge', () => {
  it('returns true when node has outgoing edge', () => {
    const edges = [createEdge('consumer', 'producer')]
    expect(hasOutgoingEdge('consumer', edges)).toBe(true)
  })

  it('returns false when node has no outgoing edge', () => {
    const edges = [createEdge('consumer', 'producer')]
    expect(hasOutgoingEdge('producer', edges)).toBe(false)
  })

  it('returns false for empty edges array', () => {
    expect(hasOutgoingEdge('consumer', [])).toBe(false)
  })
})

describe('hasIncomingEdge', () => {
  it('returns true when node has incoming edge', () => {
    const edges = [createEdge('consumer', 'producer')]
    expect(hasIncomingEdge('producer', edges)).toBe(true)
  })

  it('returns false when node has no incoming edge', () => {
    const edges = [createEdge('consumer', 'producer')]
    expect(hasIncomingEdge('consumer', edges)).toBe(false)
  })
})

describe('buildAdjacencyList', () => {
  it('builds correct adjacency list', () => {
    const edges = [
      createEdge('a', 'b'),
      createEdge('a', 'c'),
      createEdge('b', 'c'),
    ]
    const adj = buildAdjacencyList(edges)

    expect(adj.get('a')).toEqual(['b', 'c'])
    expect(adj.get('b')).toEqual(['c'])
    expect(adj.has('c')).toBe(false)
  })
})

describe('buildReverseAdjacencyList', () => {
  it('builds correct reverse adjacency list', () => {
    const edges = [
      createEdge('a', 'b'),
      createEdge('a', 'c'),
      createEdge('b', 'c'),
    ]
    const adj = buildReverseAdjacencyList(edges)

    expect(adj.get('b')).toEqual(['a'])
    expect(adj.get('c')).toEqual(['a', 'b'])
    expect(adj.has('a')).toBe(false)
  })
})

describe('isReachable', () => {
  it('returns true for directly connected nodes', () => {
    const edges = [createEdge('a', 'b')]
    expect(isReachable('a', 'b', edges)).toBe(true)
  })

  it('returns true for indirectly connected nodes', () => {
    const edges = [
      createEdge('a', 'b'),
      createEdge('b', 'c'),
      createEdge('c', 'd'),
    ]
    expect(isReachable('a', 'd', edges)).toBe(true)
  })

  it('returns false for unreachable nodes', () => {
    const edges = [
      createEdge('a', 'b'),
      createEdge('c', 'd'),
    ]
    expect(isReachable('a', 'd', edges)).toBe(false)
  })

  it('returns true when source equals target', () => {
    expect(isReachable('a', 'a', [])).toBe(true)
  })

  it('returns false for reverse direction', () => {
    const edges = [createEdge('a', 'b')]
    expect(isReachable('b', 'a', edges)).toBe(false)
  })
})

describe('getReachableNodes', () => {
  it('returns all reachable nodes', () => {
    const edges = [
      createEdge('a', 'b'),
      createEdge('b', 'c'),
      createEdge('a', 'd'),
    ]
    const reachable = getReachableNodes('a', edges)

    expect(reachable.has('a')).toBe(true)
    expect(reachable.has('b')).toBe(true)
    expect(reachable.has('c')).toBe(true)
    expect(reachable.has('d')).toBe(true)
  })

  it('does not include unreachable nodes', () => {
    const edges = [
      createEdge('a', 'b'),
      createEdge('c', 'd'),
    ]
    const reachable = getReachableNodes('a', edges)

    expect(reachable.has('a')).toBe(true)
    expect(reachable.has('b')).toBe(true)
    expect(reachable.has('c')).toBe(false)
    expect(reachable.has('d')).toBe(false)
  })
})

describe('getNodesReachingTarget', () => {
  it('returns all nodes that can reach target', () => {
    const edges = [
      createEdge('a', 'b'),
      createEdge('b', 'c'),
      createEdge('d', 'c'),
    ]
    const reaching = getNodesReachingTarget('c', edges)

    expect(reaching.has('c')).toBe(true)
    expect(reaching.has('b')).toBe(true)
    expect(reaching.has('a')).toBe(true)
    expect(reaching.has('d')).toBe(true)
  })
})

// =============================================================================
// Cycle Detection Tests
// =============================================================================

describe('hasCycle', () => {
  it('returns false for acyclic graph', () => {
    const nodes = [
      createNode('a', 'consumer'),
      createNode('b', 'filter'),
      createNode('c', 'producer'),
    ]
    const edges = [
      createEdge('a', 'b'),
      createEdge('b', 'c'),
    ]

    expect(hasCycle(nodes, edges)).toBe(false)
  })

  it('returns true for simple cycle', () => {
    const nodes = [
      createNode('a', 'consumer'),
      createNode('b', 'filter'),
    ]
    const edges = [
      createEdge('a', 'b'),
      createEdge('b', 'a'),
    ]

    expect(hasCycle(nodes, edges)).toBe(true)
  })

  it('returns true for longer cycle', () => {
    const nodes = [
      createNode('a', 'consumer'),
      createNode('b', 'filter'),
      createNode('c', 'converter'),
      createNode('d', 'producer'),
    ]
    const edges = [
      createEdge('a', 'b'),
      createEdge('b', 'c'),
      createEdge('c', 'a'), // Cycle back
      createEdge('c', 'd'),
    ]

    expect(hasCycle(nodes, edges)).toBe(true)
  })

  it('returns false for diamond-shaped DAG', () => {
    const nodes = [
      createNode('a', 'consumer'),
      createNode('b', 'filter'),
      createNode('c', 'filter'),
      createNode('d', 'producer'),
    ]
    const edges = [
      createEdge('a', 'b'),
      createEdge('a', 'c'),
      createEdge('b', 'd'),
      createEdge('c', 'd'),
    ]

    expect(hasCycle(nodes, edges)).toBe(false)
  })
})

describe('findCycle', () => {
  it('returns null for acyclic graph', () => {
    const nodes = [
      createNode('a', 'consumer'),
      createNode('b', 'producer'),
    ]
    const edges = [createEdge('a', 'b')]

    expect(findCycle(nodes, edges)).toBe(null)
  })

  it('returns cycle path for cyclic graph', () => {
    const nodes = [
      createNode('a', 'consumer'),
      createNode('b', 'filter'),
    ]
    const edges = [
      createEdge('a', 'b'),
      createEdge('b', 'a'),
    ]

    const cycle = findCycle(nodes, edges)
    expect(cycle).not.toBe(null)
    expect(cycle!.length).toBeGreaterThanOrEqual(2)
  })
})

// =============================================================================
// Orphaned Node Detection Tests
// =============================================================================

describe('findOrphanedNodes', () => {
  it('returns empty array for valid pipeline', () => {
    const nodes = [
      createNode('consumer', 'consumer'),
      createNode('filter', 'filter'),
      createNode('producer', 'producer'),
    ]
    const edges = [
      createEdge('consumer', 'filter'),
      createEdge('filter', 'producer'),
    ]

    const orphaned = findOrphanedNodes(nodes, edges, 'consumer', 'producer')
    expect(orphaned).toEqual([])
  })

  it('detects node not reachable from consumer', () => {
    const nodes = [
      createNode('consumer', 'consumer'),
      createNode('filter', 'filter'),
      createNode('producer', 'producer'),
    ]
    const edges = [
      createEdge('consumer', 'producer'),
      // filter is not connected
    ]

    const orphaned = findOrphanedNodes(nodes, edges, 'consumer', 'producer')
    expect(orphaned).toContain('filter')
  })

  it('detects node that cannot reach producer', () => {
    const nodes = [
      createNode('consumer', 'consumer'),
      createNode('filter1', 'filter'),
      createNode('filter2', 'filter'),
      createNode('producer', 'producer'),
    ]
    const edges = [
      createEdge('consumer', 'filter1'),
      createEdge('consumer', 'filter2'),
      createEdge('filter1', 'producer'),
      // filter2 cannot reach producer
    ]

    const orphaned = findOrphanedNodes(nodes, edges, 'consumer', 'producer')
    expect(orphaned).toContain('filter2')
  })

  it('does not include consumer or producer as orphaned', () => {
    const nodes = [
      createNode('consumer', 'consumer'),
      createNode('producer', 'producer'),
    ]
    const edges: Edge[] = []

    const orphaned = findOrphanedNodes(nodes, edges, 'consumer', 'producer')
    expect(orphaned).not.toContain('consumer')
    expect(orphaned).not.toContain('producer')
  })
})

describe('getNodesOnPath', () => {
  it('returns all nodes on valid path', () => {
    const nodes = [
      createNode('consumer', 'consumer'),
      createNode('filter', 'filter'),
      createNode('producer', 'producer'),
    ]
    const edges = [
      createEdge('consumer', 'filter'),
      createEdge('filter', 'producer'),
    ]

    const onPath = getNodesOnPath(nodes, edges, 'consumer', 'producer')

    expect(onPath.has('consumer')).toBe(true)
    expect(onPath.has('filter')).toBe(true)
    expect(onPath.has('producer')).toBe(true)
  })

  it('excludes orphaned nodes', () => {
    const nodes = [
      createNode('consumer', 'consumer'),
      createNode('filter', 'filter'),
      createNode('orphan', 'converter'),
      createNode('producer', 'producer'),
    ]
    const edges = [
      createEdge('consumer', 'filter'),
      createEdge('filter', 'producer'),
    ]

    const onPath = getNodesOnPath(nodes, edges, 'consumer', 'producer')

    expect(onPath.has('consumer')).toBe(true)
    expect(onPath.has('filter')).toBe(true)
    expect(onPath.has('producer')).toBe(true)
    expect(onPath.has('orphan')).toBe(false)
  })
})

// =============================================================================
// Main Validation Function Tests
// =============================================================================

describe('validatePipelineConnections', () => {
  describe('Valid Pipelines', () => {
    it('validates simple consumer -> producer pipeline', () => {
      const nodes = [
        createNode('consumer', 'consumer', 'HTTP Consumer'),
        createNode('producer', 'producer', 'File Producer'),
      ]
      const edges = [createEdge('consumer', 'producer')]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(true)
      expect(result.errors).toHaveLength(0)
    })

    it('validates consumer -> filter -> producer pipeline', () => {
      const nodes = [
        createNode('consumer', 'consumer'),
        createNode('filter', 'filter'),
        createNode('producer', 'producer'),
      ]
      const edges = [
        createEdge('consumer', 'filter'),
        createEdge('filter', 'producer'),
      ]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(true)
      expect(result.errors).toHaveLength(0)
    })

    it('validates consumer -> filter -> converter -> producer pipeline', () => {
      const nodes = [
        createNode('consumer', 'consumer'),
        createNode('filter', 'filter'),
        createNode('converter', 'converter'),
        createNode('producer', 'producer'),
      ]
      const edges = [
        createEdge('consumer', 'filter'),
        createEdge('filter', 'converter'),
        createEdge('converter', 'producer'),
      ]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(true)
      expect(result.errors).toHaveLength(0)
    })

    it('validates diamond-shaped pipeline', () => {
      const nodes = [
        createNode('consumer', 'consumer'),
        createNode('filter1', 'filter'),
        createNode('filter2', 'filter'),
        createNode('producer', 'producer'),
      ]
      const edges = [
        createEdge('consumer', 'filter1'),
        createEdge('consumer', 'filter2'),
        createEdge('filter1', 'producer'),
        createEdge('filter2', 'producer'),
      ]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(true)
      expect(result.errors).toHaveLength(0)
    })
  })

  describe('Consumer Validation', () => {
    it('fails when no consumer', () => {
      const nodes = [
        createNode('filter', 'filter'),
        createNode('producer', 'producer'),
      ]
      const edges = [createEdge('filter', 'producer')]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(false)
      expect(result.errors).toContain('Pipeline must have at least 1 Consumer')
    })

    it('allows multiple consumers', () => {
      const nodes = [
        createNode('consumer1', 'consumer'),
        createNode('consumer2', 'consumer'),
        createNode('producer', 'producer'),
      ]
      const edges = [
        createEdge('consumer1', 'producer'),
        createEdge('consumer2', 'producer'),
      ]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(true)
    })
  })

  describe('Producer Validation', () => {
    it('fails when no producer', () => {
      const nodes = [
        createNode('consumer', 'consumer'),
        createNode('filter', 'filter'),
      ]
      const edges = [createEdge('consumer', 'filter')]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(false)
      expect(result.errors).toContain('Pipeline must have at least 1 Producer')
    })

    it('allows multiple producers', () => {
      const nodes = [
        createNode('consumer', 'consumer'),
        createNode('producer1', 'producer'),
        createNode('producer2', 'producer'),
      ]
      const edges = [
        createEdge('consumer', 'producer1'),
        createEdge('consumer', 'producer2'),
      ]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(true)
    })
  })

  describe('Edge Validation', () => {
    it('fails when edge references invalid source node', () => {
      const nodes = [
        createNode('consumer', 'consumer'),
        createNode('producer', 'producer'),
      ]
      const edges = [createEdge('unknown', 'producer')]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(false)
      expect(result.errors.some((e) => e.includes('invalid source node'))).toBe(true)
    })

    it('fails when edge references invalid target node', () => {
      const nodes = [
        createNode('consumer', 'consumer'),
        createNode('producer', 'producer'),
      ]
      const edges = [createEdge('consumer', 'unknown')]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(false)
      expect(result.errors.some((e) => e.includes('invalid target node'))).toBe(true)
    })
  })

  describe('Connectivity Validation', () => {
    it('fails when consumer is isolated (no outgoing edges)', () => {
      const nodes = [
        createNode('consumer', 'consumer', 'HTTP Consumer'),
        createNode('producer', 'producer'),
      ]
      const edges: Edge[] = []

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(false)
      expect(result.errors.some((e) => e.includes('not connected to any other nodes'))).toBe(true)
    })

    it('fails when producer has no incoming edges', () => {
      const nodes = [
        createNode('consumer', 'consumer'),
        createNode('filter', 'filter'),
        createNode('producer', 'producer', 'File Producer'),
      ]
      const edges = [createEdge('consumer', 'filter')]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(false)
      expect(result.errors.some((e) => e.includes('has no incoming connections'))).toBe(true)
    })

    it('fails when producer is not reachable from consumer', () => {
      // Case where consumer and producer both have connections but no path exists
      const nodes = [
        createNode('consumer', 'consumer', 'HTTP Consumer'),
        createNode('filter1', 'filter'),
        createNode('filter2', 'filter'),
        createNode('producer', 'producer', 'File Producer'),
      ]
      const edges = [
        createEdge('consumer', 'filter1'),
        createEdge('filter2', 'producer'),
      ]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(false)
      expect(result.errors.some((e) => e.includes('not reachable from any Consumer') || e.includes('Orphaned'))).toBe(true)
    })
  })

  describe('Cycle Detection', () => {
    it('fails when graph has a simple cycle', () => {
      const nodes = [
        createNode('consumer', 'consumer'),
        createNode('filter', 'filter'),
        createNode('producer', 'producer'),
      ]
      const edges = [
        createEdge('consumer', 'filter'),
        createEdge('filter', 'consumer'), // Cycle
        createEdge('filter', 'producer'),
      ]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(false)
      expect(result.errors.some((e) => e.includes('Cycle detected'))).toBe(true)
    })

    it('fails when graph has a longer cycle', () => {
      const nodes = [
        createNode('consumer', 'consumer'),
        createNode('filter1', 'filter'),
        createNode('filter2', 'filter'),
        createNode('producer', 'producer'),
      ]
      const edges = [
        createEdge('consumer', 'filter1'),
        createEdge('filter1', 'filter2'),
        createEdge('filter2', 'filter1'), // Cycle
        createEdge('filter2', 'producer'),
      ]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(false)
      expect(result.errors.some((e) => e.includes('Cycle detected'))).toBe(true)
    })
  })

  describe('Orphaned Node Detection', () => {
    it('fails when node is not connected to main flow', () => {
      const nodes = [
        createNode('consumer', 'consumer'),
        createNode('filter', 'filter', 'Orphan Filter'),
        createNode('producer', 'producer'),
      ]
      const edges = [createEdge('consumer', 'producer')]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(false)
      expect(result.errors.some((e) => e.includes('Orphaned node'))).toBe(true)
      expect(result.errors.some((e) => e.includes('Orphan Filter'))).toBe(true)
    })

    it('fails with multiple orphaned nodes', () => {
      const nodes = [
        createNode('consumer', 'consumer'),
        createNode('filter1', 'filter', 'Filter 1'),
        createNode('filter2', 'filter', 'Filter 2'),
        createNode('producer', 'producer'),
      ]
      const edges = [createEdge('consumer', 'producer')]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(false)
      expect(result.errors.some((e) => e.includes('Orphaned node'))).toBe(true)
    })

    it('detects node reachable from consumer but not to producer', () => {
      const nodes = [
        createNode('consumer', 'consumer'),
        createNode('filter1', 'filter', 'Dead End Filter'),
        createNode('filter2', 'filter'),
        createNode('producer', 'producer'),
      ]
      const edges = [
        createEdge('consumer', 'filter1'),
        createEdge('consumer', 'filter2'),
        createEdge('filter2', 'producer'),
        // filter1 goes nowhere
      ]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(false)
      expect(result.errors.some((e) => e.includes('Dead End Filter'))).toBe(true)
    })
  })

  describe('Edge Cases', () => {
    it('handles empty nodes array', () => {
      const result = validatePipelineConnections([], [])

      expect(result.valid).toBe(false)
      expect(result.errors).toContain('Pipeline has no nodes')
    })

    it('handles null/undefined nodes', () => {
      // TypeScript would catch this but test runtime behavior
      const result = validatePipelineConnections(null as unknown as Node[], [])

      expect(result.valid).toBe(false)
    })

    it('validates with empty edges array (consumer and producer only)', () => {
      const nodes = [
        createNode('consumer', 'consumer'),
        createNode('producer', 'producer'),
      ]
      const edges: Edge[] = []

      const result = validatePipelineConnections(nodes, edges)

      expect(result.valid).toBe(false)
      // Should fail because consumer is not connected
      expect(result.errors.some((e) => e.includes('not connected'))).toBe(true)
    })
  })

  describe('Error Message Quality', () => {
    it('includes node labels in error messages', () => {
      const nodes = [
        createNode('consumer', 'consumer', 'My HTTP Consumer'),
        createNode('producer', 'producer', 'My File Producer'),
      ]
      const edges: Edge[] = []

      const result = validatePipelineConnections(nodes, edges)

      expect(result.errors.some((e) => e.includes('My HTTP Consumer'))).toBe(true)
    })

    it('provides clear error for cycle detection', () => {
      const nodes = [
        createNode('consumer', 'consumer', 'Start'),
        createNode('filter', 'filter', 'Middle'),
        createNode('producer', 'producer', 'End'),
      ]
      const edges = [
        createEdge('consumer', 'filter'),
        createEdge('filter', 'consumer'),
        createEdge('filter', 'producer'),
      ]

      const result = validatePipelineConnections(nodes, edges)

      expect(result.errors.some((e) => e.includes('Cycle detected'))).toBe(true)
      expect(result.errors.some((e) => e.includes('->'))).toBe(true)
    })
  })
})
