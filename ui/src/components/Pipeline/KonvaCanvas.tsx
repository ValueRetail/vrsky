import { useRef } from 'react'
import { Stage, Layer, Line } from 'react-konva'
import type Konva from 'konva'
import KonvaNode from './KonvaNode'
import KonvaConnection from './KonvaConnection'
import type { Node, Edge } from '../../types/pipeline'

interface KonvaCanvasProps {
  nodes: Node[]
  edges: Edge[]
  selectedNodeId: string | null
  selectedEdgeId: string | null
  connectionDrawing: boolean
  connectionStart: { nodeId: string; port: 'input' | 'output' } | null
  connectionPreviewEnd: { x: number; y: number } | null
  containerWidth: number
  onNodeDrag: (nodeId: string, x: number, y: number) => void
  onNodeSelect: (nodeId: string) => void
  onEdgeSelect: (edgeId: string) => void
  onEdgeDelete: (edgeId: string) => void
  onEdgeContextMenu: (edgeId: string, x: number, y: number) => void
  onPortMouseDown: (nodeId: string, port: 'input' | 'output', event: Konva.KonvaEventObject<MouseEvent>) => void
  onPortMouseUp: (nodeId: string, port: 'input' | 'output') => void
  onStageMouseMove: (x: number, y: number) => void
}

const GRID_SIZE = 20
const GRID_COLOR = '#e5e7eb'
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
  selectedEdgeId,
  connectionDrawing,
  connectionStart,
  connectionPreviewEnd,
  containerWidth,
  onNodeDrag,
  onNodeSelect,
  onEdgeSelect,
  onEdgeDelete,
  onEdgeContextMenu,
  onPortMouseDown,
  onPortMouseUp,
  onStageMouseMove,
}: KonvaCanvasProps) {
  const stageRef = useRef<Konva.Stage>(null)

  // Draw grid based on containerWidth (fills visible area)
  const gridLines = []
  for (let i = 0; i <= containerWidth; i += GRID_SIZE) {
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
  for (let i = 0; i <= CANVAS_HEIGHT; i += GRID_SIZE) {
    gridLines.push(
      <Line
        key={`horizontal-${i}`}
        points={[0, i, containerWidth, i]}
        stroke={GRID_COLOR}
        strokeWidth={1}
        listening={false}
      />
    )
  }

  // Handle stage mouse move (for connection preview)
  const handleStageMouseMove = () => {
    if (!connectionDrawing) return
    
    const stage = stageRef.current
    if (!stage) return
    
    // Get mouse position relative to stage
    const pointerPos = stage.getPointerPosition()
    if (pointerPos) {
      onStageMouseMove(pointerPos.x, pointerPos.y)
    }
  }

  // Handle stage click (deselect nodes and edges)
  const handleStageClick = (e: Konva.KonvaEventObject<MouseEvent>) => {
    if (e.target === e.currentTarget) {
      onNodeSelect('')
      onEdgeSelect('')
    }
  }

  return (
    <Stage
      ref={stageRef}
      width={containerWidth}
      height={CANVAS_HEIGHT}
      onMouseMove={handleStageMouseMove}
      onClick={handleStageClick}
      style={{ backgroundColor: '#f9fafb' }}
    >
      <Layer>
        {/* Grid */}
        {gridLines}

        {/* Edges/Connections */}
        {edges.map((edge) => (
          <KonvaConnection 
            key={edge.id} 
            edge={edge} 
            nodes={nodes}
            isSelected={selectedEdgeId === edge.id}
            onSelect={onEdgeSelect}
            onDelete={onEdgeDelete}
            onContextMenu={onEdgeContextMenu}
          />
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
  )
}
