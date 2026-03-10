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
  onStageMouseMove: (x: number, y: number) => void
}

const GRID_SIZE = 20
const GRID_COLOR = '#e5e7eb'
const CANVAS_WIDTH = 3000
const CANVAS_HEIGHT = 2000
const NODE_WIDTH = 120

// Generate bezier curve points for preview line (matches KonvaConnection style)
function generateBezierPoints(
  sourceX: number,
  sourceY: number,
  targetX: number,
  targetY: number
): number[] {
  const dx = targetX - sourceX
  const controlOffset = Math.max(Math.abs(dx) / 2, 50) // Minimum offset for nice curves
  const cp1x = sourceX + controlOffset
  const cp1y = sourceY
  const cp2x = targetX - controlOffset
  const cp2y = targetY

  const points: number[] = []
  const steps = 50

  for (let i = 0; i <= steps; i++) {
    const t = i / steps
    const mt = 1 - t
    const mt2 = mt * mt
    const mt3 = mt2 * mt
    const t2 = t * t
    const t3 = t2 * t

    const x = mt3 * sourceX + 3 * mt2 * t * cp1x + 3 * mt * t2 * cp2x + t3 * targetX
    const y = mt3 * sourceY + 3 * mt2 * t * cp1y + 3 * mt * t2 * cp2y + t3 * targetY

    points.push(x, y)
  }

  return points
}

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
  onStageMouseMove,
}: KonvaCanvasProps) {
  const stageRef = useRef<Konva.Stage>(null)
  const containerRef = useRef<HTMLDivElement>(null)
  // Initialize with full window dimensions to avoid 600px flash
  const [containerSize, setContainerSize] = useState({ 
    width: window.innerWidth, 
    height: window.innerHeight 
  })

  // Use ResizeObserver to track container size changes + window resize listener as backup
  useEffect(() => {
    const container = containerRef.current
    if (!container) return

    const updateSize = () => {
      // Use container dimensions if available, fall back to window dimensions
      const width = container.offsetWidth || window.innerWidth
      const height = container.offsetHeight || window.innerHeight
      setContainerSize({ width, height })
    }

    // Initial size
    updateSize()

    // Watch for container resize via ResizeObserver
    const observer = new ResizeObserver(updateSize)
    observer.observe(container)

    // Backup: window resize listener for edge cases
    window.addEventListener('resize', updateSize)

    return () => {
      observer.disconnect()
      window.removeEventListener('resize', updateSize)
    }
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

  // Handle stage mouse move (for connection preview)
  const handleStageMouseMove = (_e: Konva.KonvaEventObject<MouseEvent>) => {
    if (!connectionDrawing) return
    
    const stage = stageRef.current
    if (!stage) return
    
    // Get mouse position relative to stage (accounting for stage pan)
    const pointerPos = stage.getPointerPosition()
    if (pointerPos) {
      const stageX = stage.x()
      const stageY = stage.y()
      // Convert to canvas coordinates (accounting for stage panning)
      const x = pointerPos.x - stageX
      const y = pointerPos.y - stageY
      onStageMouseMove(x, y)
    }
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
        onMouseMove={handleStageMouseMove}
        onClick={handleStageClick}
      >
        <Layer>
          {/* Grid */}
          {gridLines}

          {/* Edges/Connections */}
          {edges.map((edge) => (
            <KonvaConnection key={edge.id} edge={edge} nodes={nodes} />
          ))}

          {/* Preview connection line while drawing (bezier curve like actual connections) */}
          {connectionDrawing && connectionStart && connectionPreviewEnd && (() => {
            const sourceNode = nodes.find((n) => n.id === connectionStart.nodeId)
            if (!sourceNode) return null
            
            // Source is the output port (right side of node)
            const sourceX = (sourceNode.position?.x || 0) + NODE_WIDTH / 2
            const sourceY = sourceNode.position?.y || 0
            const targetX = connectionPreviewEnd.x
            const targetY = connectionPreviewEnd.y
            
            const bezierPoints = generateBezierPoints(sourceX, sourceY, targetX, targetY)
            
            return (
              <Line
                points={bezierPoints}
                stroke="#f97316" // Orange like Node-RED
                strokeWidth={3}
                lineCap="round"
                lineJoin="round"
                listening={false}
              />
            )
          })()}

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
