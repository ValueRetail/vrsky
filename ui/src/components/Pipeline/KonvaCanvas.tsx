import { useRef, useState, useEffect } from 'react'
import { Stage, Layer, Line } from 'react-konva'
import type Konva from 'konva'
import KonvaNode from './KonvaNode'
import KonvaConnection from './KonvaConnection'
import type { Node, Edge } from '../../types/pipeline'

interface KonvaCanvasProps {
  nodes: Node[]
  edges: Edge[]
  selectedNodeId: string | null
  connectionDrawing: boolean
  connectionStart: { nodeId: string; port: 'input' | 'output' } | null
  connectionPreviewEnd: { x: number; y: number } | null
  onNodeDrag: (nodeId: string, x: number, y: number) => void
  onNodeSelect: (nodeId: string) => void
  onPortMouseDown: (nodeId: string, port: 'input' | 'output', event: Konva.KonvaEventObject<MouseEvent>) => void
  onPortMouseUp: (nodeId: string, port: 'input' | 'output') => void
  onStageDragMove: (x: number, y: number) => void
}

const GRID_SIZE = 20
const GRID_COLOR = '#e5e7eb'
const CANVAS_WIDTH = 3000
const CANVAS_HEIGHT = 2000

export default function KonvaCanvas({
  nodes,
  edges,
  selectedNodeId,
  connectionDrawing,
  connectionStart,
  connectionPreviewEnd,
  onNodeDrag,
  onNodeSelect,
  onPortMouseDown,
  onPortMouseUp,
  onStageDragMove,
}: KonvaCanvasProps) {
  const stageRef = useRef<Konva.Stage>(null)
  const containerRef = useRef<HTMLDivElement>(null)
  const [containerSize, setContainerSize] = useState({ width: 800, height: 600 })

  // Use ResizeObserver to track container size changes
  useEffect(() => {
    const container = containerRef.current
    if (!container) return

    const updateSize = () => {
      setContainerSize({
        width: container.offsetWidth || 800,
        height: container.offsetHeight || 600,
      })
    }

    // Initial size
    updateSize()

    // Watch for resize
    const observer = new ResizeObserver(updateSize)
    observer.observe(container)

    return () => observer.disconnect()
  }, [])

  // Draw grid
  const gridLines = []
  for (let i = 0; i < CANVAS_WIDTH; i += GRID_SIZE) {
    gridLines.push(
      <Line
        key={`vertical-${i}`}
        points={[i, 0, i, CANVAS_HEIGHT]}
        stroke={GRID_COLOR}
        strokeWidth={1}
        listening={false}
      />
    )
  }
  for (let i = 0; i < CANVAS_HEIGHT; i += GRID_SIZE) {
    gridLines.push(
      <Line
        key={`horizontal-${i}`}
        points={[0, i, CANVAS_WIDTH, i]}
        stroke={GRID_COLOR}
        strokeWidth={1}
        listening={false}
      />
    )
  }

  // Handle stage drag (panning)
  const handleStageDragMove = (e: Konva.KonvaEventObject<DragEvent>) => {
    const stage = e.currentTarget
    onStageDragMove(stage.x(), stage.y())
  }

  // Handle stage click (deselect nodes)
  const handleStageClick = (e: Konva.KonvaEventObject<MouseEvent>) => {
    if (e.target === e.currentTarget) {
      onNodeSelect('')
    }
  }

  return (
    <div
      ref={containerRef}
      className="w-full h-full overflow-hidden bg-gray-50"
    >
      <Stage
        ref={stageRef}
        width={containerSize.width}
        height={containerSize.height}
        draggable
        onDragMove={handleStageDragMove}
        onClick={handleStageClick}
      >
        <Layer>
          {/* Grid */}
          {gridLines}

          {/* Edges/Connections */}
          {edges.map((edge) => (
            <KonvaConnection key={edge.id} edge={edge} nodes={nodes} />
          ))}

          {/* Preview connection line while drawing */}
          {connectionDrawing && connectionStart && connectionPreviewEnd && (
            <Line
              points={[
                nodes.find((n) => n.id === connectionStart.nodeId)?.position?.x || 0,
                nodes.find((n) => n.id === connectionStart.nodeId)?.position?.y || 0,
                connectionPreviewEnd.x,
                connectionPreviewEnd.y,
              ]}
              stroke="#ef4444"
              strokeWidth={2}
              lineCap="round"
              listening={false}
            />
          )}

          {/* Nodes */}
          {nodes.map((node) => (
            <KonvaNode
              key={node.id}
              node={node}
              isSelected={selectedNodeId === node.id}
              onSelect={() => onNodeSelect(node.id)}
              onDrag={(x, y) => onNodeDrag(node.id, x, y)}
              onPortMouseDown={(port, event) => onPortMouseDown(node.id, port, event)}
              onPortMouseUp={(port) => onPortMouseUp(node.id, port)}
            />
          ))}
        </Layer>
      </Stage>
    </div>
  )
}
