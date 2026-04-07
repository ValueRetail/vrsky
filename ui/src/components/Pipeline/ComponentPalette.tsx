import { useState } from 'react'

interface Component {
  id: string
  type: 'input' | 'filter' | 'converter' | 'output'
  label: string
  color: string
  hoverColor: string
}

// Muted color palette matching Node-RED aesthetic
const COMPONENTS: Component[] = [
  {
    id: 'input',
    type: 'input',
    label: 'Input',
    color: '#93c5fd', // Soft blue
    hoverColor: '#7cb3f0',
  },
  {
    id: 'filter',
    type: 'filter',
    label: 'Filter',
    color: '#fdba74', // Soft orange
    hoverColor: '#f0a85e',
  },
  {
    id: 'converter',
    type: 'converter',
    label: 'Converter',
    color: '#f9a8d4', // Soft pink
    hoverColor: '#f08ec2',
  },
  {
    id: 'output',
    type: 'output',
    label: 'Output',
    color: '#86efac', // Soft green
    hoverColor: '#6de095',
  },
]

interface ComponentPaletteProps {
  onDragStart: (nodeType: string) => void
  onClose?: () => void
}

export default function ComponentPalette({ onDragStart, onClose }: ComponentPaletteProps) {
  const [hoveredId, setHoveredId] = useState<string | null>(null)
  const [draggedId, setDraggedId] = useState<string | null>(null)
  const [closeHovered, setCloseHovered] = useState(false)

  const handleDragStart = (
    e: React.DragEvent<HTMLDivElement>,
    component: Component
  ) => {
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('nodeType', component.type)
    setDraggedId(component.id)
    onDragStart(component.type)
  }

  const handleDragEnd = () => {
    setDraggedId(null)
  }

  return (
    <div
      style={{
        width: '100%',
        height: '100%',
        backgroundColor: '#f9fafb',
        display: 'flex',
        flexDirection: 'column',
      }}
    >
      {/* Header */}
      <div
        style={{
          padding: '12px 16px',
          borderBottom: '1px solid #e5e7eb',
          backgroundColor: '#f3f4f6',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
        }}
      >
        <h2
          style={{
            margin: 0,
            fontSize: '13px',
            fontWeight: 600,
            color: '#374151',
            textTransform: 'uppercase',
            letterSpacing: '0.05em',
          }}
        >
          Components
        </h2>
        {onClose && (
          <button
            onClick={onClose}
            onMouseEnter={() => setCloseHovered(true)}
            onMouseLeave={() => setCloseHovered(false)}
            style={{
              width: '24px',
              height: '24px',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              backgroundColor: closeHovered ? '#e5e7eb' : 'transparent',
              border: 'none',
              borderRadius: '4px',
              cursor: 'pointer',
              color: '#6b7280',
              fontSize: '14px',
              fontWeight: 500,
              transition: 'all 150ms ease',
            }}
            title="Close sidebar"
          >
            X
          </button>
        )}
      </div>

      {/* Component List */}
      <div
        style={{
          flex: 1,
          overflowY: 'auto',
          padding: '12px',
          display: 'flex',
          flexDirection: 'column',
          gap: '10px',
        }}
      >
        {COMPONENTS.map((component) => {
          const isHovered = hoveredId === component.id
          const isDragged = draggedId === component.id

          return (
            <div
              key={component.id}
              draggable
              onDragStart={(e) => handleDragStart(e, component)}
              onDragEnd={handleDragEnd}
              onMouseEnter={() => setHoveredId(component.id)}
              onMouseLeave={() => setHoveredId(null)}
              title={`Drag ${component.label} to canvas`}
              style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                padding: '10px 16px',
                borderRadius: '6px',
                backgroundColor: isHovered ? component.hoverColor : component.color,
                color: '#1f2937',
                fontSize: '13px',
                fontWeight: 500,
                cursor: 'grab',
                userSelect: 'none',
                transition: 'all 150ms ease',
                transform: isHovered && !isDragged ? 'scale(1.02)' : 'scale(1)',
                boxShadow: isHovered
                  ? '0 2px 4px rgba(0, 0, 0, 0.1)'
                  : '0 1px 2px rgba(0, 0, 0, 0.05)',
                border: '1px solid rgba(0, 0, 0, 0.08)',
              }}
            >
              {component.label}
            </div>
          )
        })}
      </div>
    </div>
  )
}
