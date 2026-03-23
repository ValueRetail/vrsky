import { useState } from 'react'
import { Line, Group, Circle, Text } from 'react-konva'
import type Konva from 'konva'
import type { Node, Edge } from '../../types/pipeline'

interface KonvaConnectionProps {
  edge: Edge
  nodes: Node[]
  isSelected: boolean
  onSelect: (edgeId: string) => void
  onDelete: (edgeId: string) => void
  onContextMenu: (edgeId: string, x: number, y: number) => void
}

const NODE_WIDTH = 120

export default function KonvaConnection({ 
  edge, 
  nodes, 
  isSelected, 
  onSelect, 
  onDelete,
  onContextMenu 
}: KonvaConnectionProps) {
  const [isHovered, setIsHovered] = useState(false)
  
  const sourceNode = nodes.find((n) => n.id === edge.source)
  const targetNode = nodes.find((n) => n.id === edge.target)

  if (!sourceNode || !targetNode) return null

  // Get node positions
  const sourceX = (sourceNode.position?.x || 0) + NODE_WIDTH / 2 // Output port is on the right
  const sourceY = sourceNode.position?.y || 0
  const targetX = (targetNode.position?.x || 0) - NODE_WIDTH / 2 // Input port is on the left
  const targetY = targetNode.position?.y || 0

  // Calculate bezier curve control points
  const dx = targetX - sourceX
  const controlOffset = Math.abs(dx) / 2
  const cp1x = sourceX + controlOffset
  const cp1y = sourceY
  const cp2x = targetX - controlOffset
  const cp2y = targetY

  // Create bezier curve using quadratic points
  const points: number[] = []
  const steps = 50

  // Start point
  points.push(sourceX, sourceY)

  // Generate bezier curve points and find midpoint
  let midX = 0
  let midY = 0

  for (let i = 0; i <= steps; i++) {
    const t = i / steps

    // Cubic bezier formula: P = (1-t)³*P0 + 3(1-t)²*t*P1 + 3(1-t)*t²*P2 + t³*P3
    const mt = 1 - t
    const mt2 = mt * mt
    const mt3 = mt2 * mt
    const t2 = t * t
    const t3 = t2 * t

    const x = mt3 * sourceX + 3 * mt2 * t * cp1x + 3 * mt * t2 * cp2x + t3 * targetX
    const y = mt3 * sourceY + 3 * mt2 * t * cp1y + 3 * mt * t2 * cp2y + t3 * targetY

    points.push(x, y)

    // Capture midpoint (t = 0.5)
    if (i === Math.floor(steps / 2)) {
      midX = x
      midY = y
    }
  }

  // Determine line color based on state
  const lineColor = isSelected ? '#3b82f6' : isHovered ? '#f97316' : '#9ca3af'
  const lineWidth = isSelected || isHovered ? 3 : 2

  const handleClick = (e: Konva.KonvaEventObject<MouseEvent>) => {
    e.cancelBubble = true // Prevent stage click
    if (edge.id) {
      onSelect(edge.id)
    }
  }

  const handleContextMenu = (e: Konva.KonvaEventObject<PointerEvent>) => {
    e.evt.preventDefault()
    e.cancelBubble = true
    if (!edge.id) return
    const stage = e.target.getStage()
    if (stage) {
      const pointerPos = stage.getPointerPosition()
      if (pointerPos) {
        onContextMenu(edge.id, pointerPos.x, pointerPos.y)
      }
    }
  }

  const handleDeleteClick = (e: Konva.KonvaEventObject<MouseEvent>) => {
    e.cancelBubble = true
    if (edge.id) {
      onDelete(edge.id)
    }
  }

  return (
    <Group>
      {/* Invisible wider line for easier clicking/hovering */}
      <Line
        points={points}
        stroke="transparent"
        strokeWidth={16}
        lineCap="round"
        lineJoin="round"
        onClick={handleClick}
        onContextMenu={handleContextMenu}
        onMouseEnter={() => setIsHovered(true)}
        onMouseLeave={() => setIsHovered(false)}
      />
      
      {/* Visible connection line */}
      <Line
        points={points}
        stroke={lineColor}
        strokeWidth={lineWidth}
        lineCap="round"
        lineJoin="round"
        listening={false}
      />

      {/* Delete button - shown on hover or when selected */}
      {(isHovered || isSelected) && (
        <Group
          x={midX}
          y={midY}
          onClick={handleDeleteClick}
          onMouseEnter={() => setIsHovered(true)}
        >
          {/* Red circle background */}
          <Circle
            radius={12}
            fill="#ef4444"
            stroke="#dc2626"
            strokeWidth={1}
            shadowColor="black"
            shadowBlur={4}
            shadowOpacity={0.2}
          />
          {/* X icon */}
          <Text
            text="×"
            fontSize={18}
            fontStyle="bold"
            fill="white"
            offsetX={5}
            offsetY={10}
          />
        </Group>
      )}
    </Group>
  )
}
