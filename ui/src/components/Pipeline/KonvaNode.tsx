import { useState } from 'react'
import { Group, Rect, Text, Circle } from 'react-konva'
import type Konva from 'konva'
import type { Node } from '../../types/pipeline'

interface KonvaNodeProps {
  node: Node
  isSelected: boolean
  onSelect: () => void
  onDrag: (x: number, y: number) => void
  onPortMouseDown: (port: 'input' | 'output', event: Konva.KonvaEventObject<MouseEvent>) => void
  onPortMouseUp: (port: 'input' | 'output') => void
}

const NODE_WIDTH = 120
const NODE_HEIGHT = 60
const PORT_RADIUS = 6

function getBgColor(type?: string): string {
  switch (type) {
    case 'consumer':
      return '#3b82f6' // Blue
    case 'filter':
      return '#fb923c' // Orange
    case 'converter':
      return '#ec4899' // Pink
    case 'producer':
      return '#10b981' // Emerald
    default:
      return '#6b7280' // Gray
  }
}

export default function KonvaNode({
  node,
  isSelected,
  onSelect,
  onDrag,
  onPortMouseDown,
  onPortMouseUp,
}: KonvaNodeProps) {
  const [isDragging, setIsDragging] = useState(false)

  const handleDragStart = () => {
    setIsDragging(true)
  }

  const handleDragEnd = (e: Konva.KonvaEventObject<DragEvent>) => {
    setIsDragging(false)
    const target = e.currentTarget
    onDrag(target.x(), target.y())
  }

  const bgColor = getBgColor(node.type)
  const x = node.position?.x || 0
  const y = node.position?.y || 0

  return (
    <Group
      x={x}
      y={y}
      draggable
      onDragStart={handleDragStart}
      onDragEnd={handleDragEnd}
      onClick={() => {
        if (!isDragging) {
          onSelect()
        }
      }}
      onMouseEnter={(evt) => {
        evt.currentTarget.to({ scaleX: 1.02, scaleY: 1.02, duration: 0.1 })
      }}
      onMouseLeave={(evt) => {
        evt.currentTarget.to({ scaleX: 1, scaleY: 1, duration: 0.1 })
      }}
    >
      {/* Node Rectangle */}
      <Rect
        width={NODE_WIDTH}
        height={NODE_HEIGHT}
        x={-NODE_WIDTH / 2}
        y={-NODE_HEIGHT / 2}
        fill={bgColor}
        stroke={isSelected ? '#000000' : 'none'}
        strokeWidth={isSelected ? 3 : 0}
        cornerRadius={4}
      />

      {/* Node Label */}
      <Text
        text={node.data.label}
        width={NODE_WIDTH}
        height={NODE_HEIGHT}
        x={-NODE_WIDTH / 2}
        y={-NODE_HEIGHT / 2}
        fontSize={12}
        fontWeight="bold"
        fontFamily="sans-serif"
        fill="#ffffff"
        align="center"
        verticalAlign="middle"
        listening={false}
      />

      {/* Input Port (Left Side) - Only for Filter, Converter, Producer (not Consumer) */}
      {node.type !== 'consumer' && (
        <Circle
          x={-NODE_WIDTH / 2}
          y={0}
          radius={PORT_RADIUS}
          fill="#ef4444"
          stroke="#ffffff"
          strokeWidth={2}
          onMouseDown={(evt) => onPortMouseDown('input', evt)}
          onMouseUp={() => onPortMouseUp('input')}
          onMouseEnter={(evt) => {
            evt.currentTarget.to({ radius: PORT_RADIUS + 2, duration: 0.1 })
          }}
          onMouseLeave={(evt) => {
            evt.currentTarget.to({ radius: PORT_RADIUS, duration: 0.1 })
          }}
        />
      )}

      {/* Output Port (Right Side) - Only for Consumer, Filter, Converter (not Producer) */}
      {node.type !== 'producer' && (
        <Circle
          x={NODE_WIDTH / 2}
          y={0}
          radius={PORT_RADIUS}
          fill="#ef4444"
          stroke="#ffffff"
          strokeWidth={2}
          onMouseDown={(evt) => onPortMouseDown('output', evt)}
          onMouseUp={() => onPortMouseUp('output')}
          onMouseEnter={(evt) => {
            evt.currentTarget.to({ radius: PORT_RADIUS + 2, duration: 0.1 })
          }}
          onMouseLeave={(evt) => {
            evt.currentTarget.to({ radius: PORT_RADIUS, duration: 0.1 })
          }}
        />
      )}
    </Group>
  )
}
