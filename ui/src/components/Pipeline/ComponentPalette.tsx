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
    color: 'bg-orange-400 hover:bg-orange-500',
  },
  {
    id: 'converter',
    type: 'converter',
    label: 'Converter',
    icon: '🔄',
    color: 'bg-pink-500 hover:bg-pink-600',
  },
  {
    id: 'producer',
    type: 'producer',
    label: 'Producer',
    icon: '📤',
    color: 'bg-emerald-500 hover:bg-emerald-600',
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
      <div className="p-4 border-b border-gray-300 flex items-center justify-between bg-gray-100">
        <h2 className="font-bold text-gray-900 text-base">Components</h2>
        <button
          onClick={() => setIsExpanded(!isExpanded)}
          className="p-1 hover:bg-gray-200 rounded transition-colors text-gray-600"
          title={isExpanded ? 'Collapse panel' : 'Expand panel'}
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
                 px-4 py-3 rounded-md cursor-move select-none
                 ${component.color}
                 text-white font-semibold text-sm
                 border-2 border-opacity-30 border-white
                 transition-all hover:shadow-lg hover:scale-105
                 active:opacity-80 active:scale-95
                 flex items-center justify-center
               `}
               title={`Drag ${component.label} to canvas`}
             >
               {component.label}
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
                 p-3 rounded-md cursor-move select-none
                 ${component.color}
                 text-white text-lg
                 border-2 border-opacity-30 border-white
                 transition-all hover:shadow-lg hover:scale-110
                 active:opacity-80 active:scale-95
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
