/**
 * Pipeline Validation Utilities
 *
 * This module provides comprehensive validation for pipeline graphs.
 * It enforces DAG (Directed Acyclic Graph) rules and ensures proper
 * connectivity between consumer and producer nodes.
 *
 * Validation Rules:
 * 1. Exactly 1 consumer node
 * 2. Exactly 1 producer node
 * 3. All edges reference valid nodes
 * 4. Consumer has outgoing edges (not isolated)
 * 5. Producer is reachable from consumer
 * 6. No cycles in the graph
 * 7. No orphaned nodes (nodes not on path from consumer to producer)
 */

import type { Node, Edge } from '../types/pipeline'

/**
 * Result of pipeline validation
 */
export interface ValidationResult {
  valid: boolean
  errors: string[]
  warnings: string[]
}

/**
 * Check if a node has at least one outgoing edge
 */
export function hasOutgoingEdge(nodeId: string, edges: Edge[]): boolean {
  return edges.some((edge) => edge.source === nodeId)
}

/**
 * Check if a node has at least one incoming edge
 */
export function hasIncomingEdge(nodeId: string, edges: Edge[]): boolean {
  return edges.some((edge) => edge.target === nodeId)
}

/**
 * Build adjacency list from edges (for forward traversal)
 */
export function buildAdjacencyList(edges: Edge[]): Map<string, string[]> {
  const adjacency = new Map<string, string[]>()

  for (const edge of edges) {
    if (!adjacency.has(edge.source)) {
      adjacency.set(edge.source, [])
    }
    adjacency.get(edge.source)!.push(edge.target)
  }

  return adjacency
}

/**
 * Build reverse adjacency list from edges (for backward traversal)
 */
export function buildReverseAdjacencyList(edges: Edge[]): Map<string, string[]> {
  const adjacency = new Map<string, string[]>()

  for (const edge of edges) {
    if (!adjacency.has(edge.target)) {
      adjacency.set(edge.target, [])
    }
    adjacency.get(edge.target)!.push(edge.source)
  }

  return adjacency
}

/**
 * Check if targetId is reachable from sourceId using BFS
 */
export function isReachable(sourceId: string, targetId: string, edges: Edge[]): boolean {
  if (sourceId === targetId) {
    return true
  }

  const adjacency = buildAdjacencyList(edges)
  const visited = new Set<string>()
  const queue: string[] = [sourceId]

  while (queue.length > 0) {
    const current = queue.shift()!

    if (current === targetId) {
      return true
    }

    if (visited.has(current)) {
      continue
    }
    visited.add(current)

    const neighbors = adjacency.get(current) || []
    for (const neighbor of neighbors) {
      if (!visited.has(neighbor)) {
        queue.push(neighbor)
      }
    }
  }

  return false
}

/**
 * Get all nodes reachable from a source node using BFS
 */
export function getReachableNodes(sourceId: string, edges: Edge[]): Set<string> {
  const adjacency = buildAdjacencyList(edges)
  const visited = new Set<string>()
  const queue: string[] = [sourceId]

  while (queue.length > 0) {
    const current = queue.shift()!

    if (visited.has(current)) {
      continue
    }
    visited.add(current)

    const neighbors = adjacency.get(current) || []
    for (const neighbor of neighbors) {
      if (!visited.has(neighbor)) {
        queue.push(neighbor)
      }
    }
  }

  return visited
}

/**
 * Get all nodes that can reach a target node (backward BFS)
 */
export function getNodesReachingTarget(targetId: string, edges: Edge[]): Set<string> {
  const reverseAdjacency = buildReverseAdjacencyList(edges)
  const visited = new Set<string>()
  const queue: string[] = [targetId]

  while (queue.length > 0) {
    const current = queue.shift()!

    if (visited.has(current)) {
      continue
    }
    visited.add(current)

    const predecessors = reverseAdjacency.get(current) || []
    for (const predecessor of predecessors) {
      if (!visited.has(predecessor)) {
        queue.push(predecessor)
      }
    }
  }

  return visited
}

/**
 * Detect if the graph has a cycle using DFS
 * Returns the cycle path if found, or null if no cycle
 */
export function findCycle(nodes: Node[], edges: Edge[]): string[] | null {
  const adjacency = buildAdjacencyList(edges)
  const nodeIds = new Set(nodes.map((n) => n.id))

  // Track visited and recursion stack
  const visited = new Set<string>()
  const recStack = new Set<string>()
  const parent = new Map<string, string>()

  function dfs(nodeId: string): string | null {
    visited.add(nodeId)
    recStack.add(nodeId)

    const neighbors = adjacency.get(nodeId) || []
    for (const neighbor of neighbors) {
      if (!nodeIds.has(neighbor)) {
        continue // Skip invalid neighbors
      }

      if (!visited.has(neighbor)) {
        parent.set(neighbor, nodeId)
        const cycleStart = dfs(neighbor)
        if (cycleStart !== null) {
          return cycleStart
        }
      } else if (recStack.has(neighbor)) {
        // Found cycle - return the start of the cycle
        parent.set(neighbor, nodeId)
        return neighbor
      }
    }

    recStack.delete(nodeId)
    return null
  }

  // Run DFS from all unvisited nodes
  for (const node of nodes) {
    if (!visited.has(node.id)) {
      const cycleStart = dfs(node.id)
      if (cycleStart !== null) {
        // Reconstruct cycle path
        const cyclePath: string[] = [cycleStart]
        let current = parent.get(cycleStart)
        while (current && current !== cycleStart) {
          cyclePath.unshift(current)
          current = parent.get(current)
        }
        cyclePath.unshift(cycleStart) // Complete the cycle
        return cyclePath
      }
    }
  }

  return null
}

/**
 * Check if graph has a cycle (simple boolean check)
 */
export function hasCycle(nodes: Node[], edges: Edge[]): boolean {
  return findCycle(nodes, edges) !== null
}

/**
 * Find orphaned nodes - nodes not on the path from consumer to producer
 *
 * A node is orphaned if:
 * - It's not reachable from the consumer, OR
 * - It cannot reach the producer
 */
export function findOrphanedNodes(
  nodes: Node[],
  edges: Edge[],
  consumerId: string,
  producerId: string
): string[] {
  // Nodes reachable from consumer (forward)
  const reachableFromConsumer = getReachableNodes(consumerId, edges)

  // Nodes that can reach producer (backward)
  const canReachProducer = getNodesReachingTarget(producerId, edges)

  const orphaned: string[] = []

  for (const node of nodes) {
    // Consumer and producer are never orphaned
    if (node.id === consumerId || node.id === producerId) {
      continue
    }

    // A node is on the valid path if it's reachable from consumer AND can reach producer
    const onPath = reachableFromConsumer.has(node.id) && canReachProducer.has(node.id)

    if (!onPath) {
      orphaned.push(node.id)
    }
  }

  return orphaned
}

/**
 * Get a user-friendly label for a node
 */
function getNodeLabel(node: Node): string {
  return node.data?.label || node.id
}

/**
 * Main validation function - validates entire pipeline
 */
export function validatePipelineConnections(nodes: Node[], edges: Edge[]): ValidationResult {
  const errors: string[] = []
  const warnings: string[] = []

  // Early return if no nodes
  if (!nodes || nodes.length === 0) {
    return {
      valid: false,
      errors: ['Pipeline has no nodes'],
      warnings: [],
    }
  }

  // Rule 1: At least 1 consumer
  const consumers = nodes.filter((n) => n.type === 'consumer')
  if (consumers.length === 0) {
    errors.push('Pipeline must have at least 1 Consumer')
  }

  // Rule 2: At least 1 producer
  const producers = nodes.filter((n) => n.type === 'producer')
  if (producers.length === 0) {
    errors.push('Pipeline must have at least 1 Producer')
  }

  // If we don't have at least 1 consumer and 1 producer, can't continue validation
  if (consumers.length === 0 || producers.length === 0) {
    return { valid: false, errors, warnings }
  }

  const consumer = consumers[0]
  const producer = producers[0]

  // Rule 3: All edges reference valid nodes
  const nodeIds = new Set(nodes.map((n) => n.id))
  for (const edge of edges) {
    if (!nodeIds.has(edge.source)) {
      errors.push(`Edge references invalid source node: '${edge.source}'`)
    }
    if (!nodeIds.has(edge.target)) {
      errors.push(`Edge references invalid target node: '${edge.target}'`)
    }
  }

  // If we have invalid edge references, can't continue validation
  if (errors.length > 2 - (consumers.length === 1 ? 0 : 1) - (producers.length === 1 ? 0 : 1)) {
    // We added errors beyond consumer/producer checks
    const invalidEdgeErrors = errors.filter((e) => e.includes('Edge references invalid'))
    if (invalidEdgeErrors.length > 0) {
      return { valid: false, errors, warnings }
    }
  }

  // Rule 4: Each consumer has outgoing edges
  for (const c of consumers) {
    if (!hasOutgoingEdge(c.id, edges)) {
      errors.push(`Consumer '${getNodeLabel(c)}' is not connected to any other nodes`)
    }
  }

  // Rule 5: Each producer has incoming edges
  for (const p of producers) {
    if (!hasIncomingEdge(p.id, edges)) {
      errors.push(`Producer '${getNodeLabel(p)}' has no incoming connections`)
    }
  }

  // Rule 6: Each producer is reachable from at least one consumer
  for (const p of producers) {
    if (!hasIncomingEdge(p.id, edges)) continue
    const reachable = consumers.some((c) => hasOutgoingEdge(c.id, edges) && isReachable(c.id, p.id, edges))
    if (!reachable) {
      errors.push(`Producer '${getNodeLabel(p)}' is not reachable from any Consumer`)
    }
  }

  // Rule 7: No cycles
  const cyclePath = findCycle(nodes, edges)
  if (cyclePath !== null) {
    // Get labels for cycle nodes
    const cycleLabels = cyclePath.map((id) => {
      const node = nodes.find((n) => n.id === id)
      return node ? getNodeLabel(node) : id
    })
    errors.push(`Cycle detected in pipeline: ${cycleLabels.join(' -> ')}`)
  }

  // Rule 8: No orphaned nodes (only check if no cycles)
  if (cyclePath === null) {
    // Collect all nodes reachable from any consumer
    const reachableFromConsumers = new Set<string>()
    for (const c of consumers) {
      const visited = new Set<string>()
      const queue = [c.id]
      while (queue.length > 0) {
        const current = queue.shift()!
        if (visited.has(current)) continue
        visited.add(current)
        reachableFromConsumers.add(current)
        for (const edge of edges) {
          if (edge.source === current) queue.push(edge.target)
        }
      }
    }
    // Collect all nodes that can reach any producer (reverse traversal)
    const canReachProducers = new Set<string>()
    for (const p of producers) {
      const visited = new Set<string>()
      const queue = [p.id]
      while (queue.length > 0) {
        const current = queue.shift()!
        if (visited.has(current)) continue
        visited.add(current)
        canReachProducers.add(current)
        for (const edge of edges) {
          if (edge.target === current) queue.push(edge.source)
        }
      }
    }
    const orphaned = nodes
      .filter((n) => !reachableFromConsumers.has(n.id) || !canReachProducers.has(n.id))
      .map((n) => n.id)
    if (orphaned.length > 0) {
      const orphanedLabels = orphaned.map((id) => {
        const node = nodes.find((n) => n.id === id)
        return node ? getNodeLabel(node) : id
      })
      errors.push(`Orphaned node(s) not connected to data flow: ${orphanedLabels.join(', ')}`)
    }
  }

  return {
    valid: errors.length === 0,
    errors,
    warnings,
  }
}

/**
 * Get nodes that are on the valid data path (for visual highlighting)
 */
export function getNodesOnPath(
  nodes: Node[],
  edges: Edge[],
  consumerId: string,
  producerId: string
): Set<string> {
  const reachableFromConsumer = getReachableNodes(consumerId, edges)
  const canReachProducer = getNodesReachingTarget(producerId, edges)

  const onPath = new Set<string>()

  for (const node of nodes) {
    if (reachableFromConsumer.has(node.id) && canReachProducer.has(node.id)) {
      onPath.add(node.id)
    }
  }

  return onPath
}
