import { useCallback, useState, useEffect } from 'react'
import type Konva from 'konva'
import type { Node, Edge } from '../types/pipeline'

export function useConnectionDrawing(
  _nodes: Node[],
  setEdges: (edges: Edge[]) => void,
  edges: Edge[]
) {
  const [connectionDrawing, setConnectionDrawing] = useState(false)
  const [connectionStart, setConnectionStart] = useState<{
    nodeId: string
    port: 'input' | 'output'
  } | null>(null)
  const [connectionPreviewEnd, setConnectionPreviewEnd] = useState<{ x: number; y: number } | null>(
    null
  )

  // Handle port mouse down (start drawing connection)
  const handlePortMouseDown = useCallback(
    (nodeId: string, port: 'input' | 'output', event: Konva.KonvaEventObject<MouseEvent>) => {
      event.cancelBubble = true

      // Can't start from input port
      if (port === 'input') return

      setConnectionDrawing(true)
      setConnectionStart({ nodeId, port })
    },
    []
  )

  // Handle port mouse up (end drawing connection)
  const handlePortMouseUp = useCallback(
    (nodeId: string, port: 'input' | 'output') => {
      if (!connectionDrawing || !connectionStart) return

      // Can't connect to output port
      if (port === 'output') return

      // Can't connect a node to itself
      if (nodeId === connectionStart.nodeId) {
        setConnectionDrawing(false)
        setConnectionStart(null)
        setConnectionPreviewEnd(null)
        return
      }

      // Check if connection already exists
      const connectionExists = edges.some(
        (e) => e.source === connectionStart.nodeId && e.target === nodeId
      )

      if (!connectionExists) {
        // Create new edge
        const newEdge: Edge = {
          id: `${connectionStart.nodeId}-${nodeId}`,
          source: connectionStart.nodeId,
          target: nodeId,
        }
        setEdges([...edges, newEdge])
      }

      setConnectionDrawing(false)
      setConnectionStart(null)
      setConnectionPreviewEnd(null)
    },
    [connectionDrawing, connectionStart, edges, setEdges]
  )

  // Handle stage mouse move (update preview line)
  const handleStageDragMove = useCallback((_stageX: number, _stageY: number) => {
    if (!connectionDrawing || !connectionStart) {
      setConnectionPreviewEnd(null)
      return
    }

    // Get the stage element to calculate mouse position
    if (typeof window !== 'undefined') {
      const stageElement = document.querySelector('canvas')
      if (stageElement) {
        const rect = stageElement.getBoundingClientRect()
        const mouseX = (window.innerWidth - rect.left) / 2 // Approximate
        const mouseY = (window.innerHeight - rect.top) / 2 // Approximate

        setConnectionPreviewEnd({ x: mouseX, y: mouseY })
      }
    }
  }, [connectionDrawing, connectionStart])

  // Handle ESC key to cancel connection drawing
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && connectionDrawing) {
        setConnectionDrawing(false)
        setConnectionStart(null)
        setConnectionPreviewEnd(null)
      }
    }

    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [connectionDrawing])

  return {
    connectionDrawing,
    connectionStart,
    connectionPreviewEnd,
    setConnectionPreviewEnd,
    handlePortMouseDown,
    handlePortMouseUp,
    handleStageDragMove,
  }
}
