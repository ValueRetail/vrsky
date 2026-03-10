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

  // Handle mouse move on stage (update preview line position)
  const handleStageMouseMove = useCallback(
    (x: number, y: number) => {
      if (connectionDrawing && connectionStart) {
        setConnectionPreviewEnd({ x, y })
      }
    },
    [connectionDrawing, connectionStart]
  )

  // Cancel connection drawing (called on mouseup anywhere or ESC)
  const cancelConnectionDrawing = useCallback(() => {
    if (connectionDrawing) {
      setConnectionDrawing(false)
      setConnectionStart(null)
      setConnectionPreviewEnd(null)
    }
  }, [connectionDrawing])

  // Handle ESC key to cancel connection drawing
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && connectionDrawing) {
        cancelConnectionDrawing()
      }
    }

    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [connectionDrawing, cancelConnectionDrawing])

  // Handle global mouseup to cancel connection if not released on a valid port
  useEffect(() => {
    const handleGlobalMouseUp = () => {
      // Small delay to allow port mouseup to fire first
      setTimeout(() => {
        if (connectionDrawing) {
          cancelConnectionDrawing()
        }
      }, 10)
    }

    if (connectionDrawing) {
      window.addEventListener('mouseup', handleGlobalMouseUp)
      return () => window.removeEventListener('mouseup', handleGlobalMouseUp)
    }
  }, [connectionDrawing, cancelConnectionDrawing])

  return {
    connectionDrawing,
    connectionStart,
    connectionPreviewEnd,
    setConnectionPreviewEnd,
    handlePortMouseDown,
    handlePortMouseUp,
    handleStageMouseMove,
    cancelConnectionDrawing,
  }
}
