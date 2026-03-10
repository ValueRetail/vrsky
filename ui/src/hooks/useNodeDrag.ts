import { useCallback } from 'react'
import type { Node } from '../types/pipeline'

const GRID_SIZE = 20

export function useNodeDrag(nodes: Node[], setNodes: (nodes: Node[]) => void) {
  const handleNodeDrag = useCallback(
    (nodeId: string, x: number, y: number) => {
      // Snap to grid
      const snappedX = Math.round(x / GRID_SIZE) * GRID_SIZE
      const snappedY = Math.round(y / GRID_SIZE) * GRID_SIZE

      // Update node position in state
      setNodes(
        nodes.map((node) =>
          node.id === nodeId
            ? {
                ...node,
                position: { x: snappedX, y: snappedY },
              }
            : node
        )
      )
    },
    [nodes, setNodes]
  )

  return { handleNodeDrag }
}
