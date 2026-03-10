import { Line } from 'react-konva'
import type { Node, Edge } from '../../types/pipeline'

interface KonvaConnectionProps {
  edge: Edge
  nodes: Node[]
}

const NODE_WIDTH = 120

export default function KonvaConnection({ edge, nodes }: KonvaConnectionProps) {
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

  // Generate bezier curve points
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
  }

  return (
    <Line
      points={points}
      stroke="#9ca3af"
      strokeWidth={2}
      lineCap="round"
      lineJoin="round"
      listening={false}
    />
  )
}
