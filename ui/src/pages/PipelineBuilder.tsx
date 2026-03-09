import { useState, useCallback, useRef } from 'react'
import KonvaCanvas from '../components/Pipeline/KonvaCanvas'
import PropertyEditor from '../components/Pipeline/PropertyEditor'
import ComponentPalette from '../components/Pipeline/ComponentPalette'
import apiClient from '../services/api'
import { useUIStore } from '../store/uiStore'
import { getNodeLabel, renumberNodesAfterDeletion } from '../utils/nodeNumbering'
import { useNodeDrag } from '../hooks/useNodeDrag'
import { useConnectionDrawing } from '../hooks/useConnectionDrawing'
import type { Node, Edge } from '../types/pipeline'

export default function PipelineBuilder() {
  const [nodes, setNodes] = useState<Node[]>([])
  const [edges, setEdges] = useState<Edge[]>([])
  const [selectedNode, setSelectedNode] = useState<Node | null>(null)
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
  const [isLoading, setIsLoading] = useState(false)
  const [stagePos, setStagePos] = useState({ x: 0, y: 0 })
  const canvasContainer = useRef<HTMLDivElement>(null)
  const { showErrorNotification, showSuccessNotification } = useUIStore()

  // Use custom hooks
  const { handleNodeDrag } = useNodeDrag(nodes, setNodes)
  const {
    connectionDrawing,
    connectionStart,
    connectionPreviewEnd,
    setConnectionPreviewEnd,
    handlePortMouseDown,
    handlePortMouseUp,
  } = useConnectionDrawing(nodes, setEdges, edges)

  const handleDragOver = useCallback((event: React.DragEvent<HTMLDivElement>) => {
    event.preventDefault()
    event.dataTransfer.dropEffect = 'move'
  }, [])

  const handleDrop = useCallback(
    (event: React.DragEvent<HTMLDivElement>) => {
      event.preventDefault()

      if (!canvasContainer.current) return

      const nodeType = event.dataTransfer.getData('nodeType') as
        | 'consumer'
        | 'filter'
        | 'converter'
        | 'producer'

      if (!nodeType) return

      // Get canvas position (relative to the canvas, accounting for stage position)
      const rect = canvasContainer.current.getBoundingClientRect()
      const x = event.clientX - rect.left - stagePos.x
      const y = event.clientY - rect.top - stagePos.y

      // Snap to grid
      const GRID_SIZE = 20
      const snappedX = Math.round(x / GRID_SIZE) * GRID_SIZE
      const snappedY = Math.round(y / GRID_SIZE) * GRID_SIZE

      // Create new node with auto-numbered label
      const label = getNodeLabel(nodeType, nodes)
      const newNode: Node = {
        id: `${nodeType}-${Date.now()}-${Math.random()}`,
        type: nodeType,
        data: {
          label,
          type: nodeType,
          config: {},
        },
        position: { x: snappedX, y: snappedY },
      }

      setNodes((nds) => [...nds, newNode])
    },
    [nodes, stagePos]
  )

  const updateNodeConfig = (config: Record<string, unknown>) => {
    if (!selectedNode) return

    setNodes((nds) =>
      nds.map((node) =>
        node.id === selectedNode.id
          ? { ...node, data: { ...node.data, config } }
          : node
      )
    )
    setSelectedNode(null)
    setSelectedNodeId(null)
  }

  const handleNodeDelete = useCallback(() => {
    if (!selectedNode) return

    // Remove node and its edges
    setNodes((nds) => nds.filter((n) => n.id !== selectedNode.id))
    setEdges((eds) =>
      eds.filter((e) => e.source !== selectedNode.id && e.target !== selectedNode.id)
    )

    // Renumber remaining nodes
    setNodes((nds) => renumberNodesAfterDeletion(nds, selectedNode.id))
    setSelectedNode(null)
    setSelectedNodeId(null)
  }, [selectedNode])

  const handleClosePropertyEditor = useCallback(() => {
    setSelectedNode(null)
    setSelectedNodeId(null)
  }, [])

  const validatePipeline = (): boolean => {
    const consumers = nodes.filter((n) => n.type === 'consumer')
    const producers = nodes.filter((n) => n.type === 'producer')

    if (consumers.length === 0) {
      showErrorNotification('Pipeline validation', 'Pipeline must have at least one Consumer')
      return false
    }
    if (producers.length === 0) {
      showErrorNotification('Pipeline validation', 'Pipeline must have at least one Producer')
      return false
    }

    // Check that consumer and producer are configured
    const consumerConfigured = consumers.some((n) => n.data.config && Object.keys(n.data.config).length > 0)
    const producerConfigured = producers.some((n) => n.data.config && Object.keys(n.data.config).length > 0)

    if (!consumerConfigured) {
      showErrorNotification('Pipeline validation', 'Configure Consumer node before deploying')
      return false
    }
    if (!producerConfigured) {
      showErrorNotification('Pipeline validation', 'Configure Producer node before deploying')
      return false
    }

    return true
  }

  const buildConnectionPayload = () => {
    const consumer = nodes.find((n) => n.type === 'consumer')
    const producer = nodes.find((n) => n.type === 'producer')

    return {
      name: `Pipeline ${new Date().toLocaleTimeString()}`,
      description: 'Created via visual pipeline editor',
      source_config: consumer?.data.config || {},
      destination_config: producer?.data.config || {},
      converter_config: {},
      filter_config: {},
    }
  }

  const deployPipeline = async () => {
    if (!validatePipeline()) return

    const payload = buildConnectionPayload()
    setIsLoading(true)

    try {
      await apiClient.post('/api/v1/connections', payload)
      showSuccessNotification('Success', 'Pipeline deployed successfully!')
      // Reset canvas
      setNodes([])
      setEdges([])
      setSelectedNode(null)
      setSelectedNodeId(null)
    } catch (error) {
      const errorMsg = error instanceof Error ? error.message : 'Unknown error'
      showErrorNotification('Deployment failed', errorMsg)
      console.error('Deploy error:', error)
    } finally {
      setIsLoading(false)
    }
  }

  return (
    <div className="h-screen w-screen flex flex-col overflow-hidden bg-gray-100">
      {/* Deploy Button - Top Right (Node-RED style) */}
      <button
        onClick={deployPipeline}
        disabled={isLoading}
        className="absolute top-4 right-4 z-50 px-6 py-2 bg-red-600 hover:bg-red-700 disabled:bg-gray-400 text-white font-semibold rounded-lg transition-colors shadow-lg flex items-center gap-2"
      >
        {isLoading ? (
          <>
            <span className="animate-spin">⚙️</span>
            Deploying...
          </>
        ) : (
          <>Deploy</>
        )}
      </button>

      {/* Main Content Area - Flex Row */}
      <div className="flex flex-1 overflow-hidden">
        {/* Left Sidebar - Component Palette */}
        <div className="w-64 bg-white border-r border-gray-200 flex-shrink-0 overflow-y-auto">
          <ComponentPalette onDragStart={() => {}} />
        </div>

        {/* Center - Canvas Area */}
        <div
          ref={canvasContainer}
          className="flex-1 overflow-hidden relative"
          onDragOver={handleDragOver}
          onDrop={handleDrop}
        >
          <KonvaCanvas
            nodes={nodes}
            edges={edges}
            selectedNodeId={selectedNodeId}
            connectionDrawing={connectionDrawing}
            connectionStart={connectionStart}
            connectionPreviewEnd={connectionPreviewEnd}
            onNodeDrag={handleNodeDrag}
            onNodeSelect={(nodeId) => {
              setSelectedNodeId(nodeId)
              const node = nodes.find((n) => n.id === nodeId)
              if (node) {
                setSelectedNode(node)
              } else {
                // Clicked on canvas background
                setSelectedNode(null)
              }
            }}
            onPortMouseDown={handlePortMouseDown}
            onPortMouseUp={handlePortMouseUp}
            onStageDragMove={(x, y) => {
              setStagePos({ x, y })
              // Update connection preview while drawing
              if (connectionDrawing) {
                setConnectionPreviewEnd({ x: -x, y: -y })
              }
            }}
          />
        </div>

        {/* Right Sidebar - Property Editor (Slide In/Out) */}
        <div
          className={`
            bg-white border-l border-gray-200 overflow-hidden flex-shrink-0
            transition-all duration-300 ease-in-out
            ${selectedNode ? 'w-96' : 'w-0'}
          `}
        >
          {selectedNode && (
            <div className="w-96 h-full overflow-y-auto">
              <PropertyEditor
                node={selectedNode}
                onUpdate={updateNodeConfig}
                onClose={handleClosePropertyEditor}
                onDelete={handleNodeDelete}
              />
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
