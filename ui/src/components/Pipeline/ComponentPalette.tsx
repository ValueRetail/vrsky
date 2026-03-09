import { useState } from 'react'

interface Component {
  id: string
  type: 'consumer' | 'filter' | 'converter' | 'producer'
  label: string
  icon: string
  color: string
}

const COMPONENTS: Component[] = [
  {
    id: 'consumer',
    type: 'consumer',
    label: 'Consumer',
    icon: '📥',
    color: 'bg-blue-500 hover:bg-blue-600',
  },
  {
    id: 'filter',
    type: 'filter',
    label: 'Filter',
    icon: '🔍',
    color: 'bg-yellow-500 hover:bg-yellow-600',
  },
  {
    id: 'converter',
    type: 'converter',
    label: 'Converter',
    icon: '🔄',
    color: 'bg-purple-500 hover:bg-purple-600',
  },
  {
    id: 'producer',
    type: 'producer',
    label: 'Producer',
    icon: '📤',
    color: 'bg-green-500 hover:bg-green-600',
  },
]

interface ComponentPaletteProps {
  onDragStart: (nodeType: string) => void
}

export default function ComponentPalette({ onDragStart }: ComponentPaletteProps) {
  const [isExpanded, setIsExpanded] = useState(true)

  const handleDragStart = (
    e: React.DragEvent<HTMLDivElement>,
    component: Component
  ) => {
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('nodeType', component.type)
    onDragStart(component.type)
  }

  return (
    <div className="h-full bg-white border-r border-gray-200 flex flex-col shadow-sm">
      {/* Header */}
      <div className="p-4 border-b border-gray-200 flex items-center justify-between">
        <h2 className="font-bold text-gray-800">Components</h2>
        <button
          onClick={() => setIsExpanded(!isExpanded)}
          className="p-1 hover:bg-gray-100 rounded transition-colors"
          title={isExpanded ? 'Collapse' : 'Expand'}
        >
          {isExpanded ? '▼' : '▶'}
        </button>
      </div>

      {/* Component List */}
      {isExpanded && (
        <div className="flex-1 overflow-y-auto p-3 space-y-2">
          {COMPONENTS.map((component) => (
            <div
              key={component.id}
              draggable
              onDragStart={(e) => handleDragStart(e, component)}
              className={`
                p-3 rounded-lg cursor-move
                ${component.color}
                text-white font-medium text-sm
                transition-all hover:shadow-md active:opacity-80
                flex items-center gap-2
                select-none
              `}
              title={`Drag ${component.label} to canvas`}
            >
              <span className="text-lg">{component.icon}</span>
              <span>{component.label}</span>
            </div>
          ))}
        </div>
      )}

      {/* Collapsed state - show icons only */}
      {!isExpanded && (
        <div className="flex-1 overflow-y-auto p-2 space-y-2 flex flex-col items-center">
          {COMPONENTS.map((component) => (
            <div
              key={component.id}
              draggable
              onDragStart={(e) => handleDragStart(e, component)}
              className={`
                p-2 rounded-lg cursor-move
                ${component.color}
                text-white text-xl
                transition-all hover:shadow-md active:opacity-80
                select-none
              `}
              title={`Drag ${component.label} to canvas`}
            >
              {component.icon}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
