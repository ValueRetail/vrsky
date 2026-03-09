import type { Node } from 'reactflow'

interface NodeData {
  type?: string
  label?: string
  config?: Record<string, unknown>
}

/**
 * Generates an auto-numbered label for a node based on type and existing nodes
 * Example: "Consumer 1", "Filter 2", etc.
 */
export function getNodeLabel(nodeType: string, allNodes: Node<NodeData>[]): string {
  const sameTypeCount = allNodes.filter((n) => n.data.type === nodeType).length + 1
  const capitalizedType = nodeType.charAt(0).toUpperCase() + nodeType.slice(1)
  return `${capitalizedType} ${sameTypeCount}`
}

/**
 * Re-numbers all remaining nodes after one is deleted
 * Ensures no gaps in numbering (Consumer 1, 2, 3, not 1, 3, 5)
 */
export function renumberNodesAfterDeletion(
  nodes: Node<NodeData>[],
  _deletedNodeId: string
): Node<NodeData>[] {
  // Group remaining nodes by type
  const nodesByType: Record<string, Node<NodeData>[]> = {}

  nodes.forEach((node) => {
    const type = node.data.type || 'unknown'
    if (!nodesByType[type]) {
      nodesByType[type] = []
    }
    nodesByType[type].push(node)
  })

  // Re-number each type's nodes sequentially
  return nodes.map((node) => {
    const type = node.data.type || 'unknown'
    const sameTypeNodes = nodesByType[type] || []
    const index = sameTypeNodes.findIndex((n) => n.id === node.id) + 1

    return {
      ...node,
      data: {
        ...node.data,
        label: `${type.charAt(0).toUpperCase() + type.slice(1)} ${index}`,
      },
    }
  })
}
