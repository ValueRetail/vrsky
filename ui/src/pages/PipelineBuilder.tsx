import { useState, useCallback, useRef } from 'react'
import {
  ReactFlow,
  addEdge,
  useNodesState,
  useEdgesState,
  Background,
  Controls,
} from 'reactflow'
import type { Node, Connection } from 'reactflow'
import 'reactflow/dist/style.css'
import ConsumerNode from '../components/Pipeline/ConsumerNode'
import FilterNode from '../components/Pipeline/FilterNode'
import ConverterNode from '../components/Pipeline/ConverterNode'
import ProducerNode from '../components/Pipeline/ProducerNode'
import PropertyEditor from '../components/Pipeline/PropertyEditor'
import ComponentPalette from '../components/Pipeline/ComponentPalette'
import apiClient from '../services/api'
import { useUIStore } from '../store/uiStore'
import { getNodeLabel, renumberNodesAfterDeletion } from '../utils/nodeNumbering'

const nodeTypes = {
  consumer: ConsumerNode,
  filter: FilterNode,
  converter: ConverterNode,
  producer: ProducerNode,
}

interface NodeData {
  label: string
  config?: Record<string, unknown>
  type?: string
}

export default function PipelineBuilder() {
  const [nodes, setNodes, onNodesChange] = useNodesState([])
  const [edges, setEdges, onEdgesChange] = useEdgesState([])
  const [selectedNode, setSelectedNode] = useState<Node<NodeData> | null>(null)
  const [isLoading, setIsLoading] = useState(false)
  const reactFlowWrapper = useRef<HTMLDivElement>(null)
  const { showErrorNotification, showSuccessNotification } = useUIStore()

  const onConnect = useCallback(
    (connection: Connection) => {
      setEdges((eds) => addEdge(connection, eds))
    },
    [setEdges]
  )

  const onNodeClick = useCallback((_event: React.MouseEvent, node: Node<NodeData>) => {
    setSelectedNode(node)
  }, [])

  const handleDragOver = useCallback((event: React.DragEvent<HTMLDivElement>) => {
    event.preventDefault()
    event.dataTransfer.dropEffect = 'move'
  }, [])

  const handleDrop = useCallback(
    (event: React.DragEvent<HTMLDivElement>) => {
      event.preventDefault()

      if (!reactFlowWrapper.current) return

      const nodeType = event.dataTransfer.getData('nodeType') as
        | 'consumer'
        | 'filter'
        | 'converter'
        | 'producer'

      if (!nodeType) return

      // Get canvas position
      const rect = reactFlowWrapper.current.getBoundingClientRect()
      const x = event.clientX - rect.left
      const y = event.clientY - rect.top

      // Create new node with auto-numbered label
      const label = getNodeLabel(nodeType, nodes)
      const newNode: Node<NodeData> = {
        id: `${nodeType}-${Date.now()}-${Math.random()}`,
        type: nodeType,
        data: {
          label,
          type: nodeType,
          config: {},
        },
        position: { x, y },
      }

      setNodes((nds) => [...nds, newNode])
    },
    [nodes, setNodes]
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
  }, [selectedNode, setNodes, setEdges])

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
    } catch (error) {
      const errorMsg = error instanceof Error ? error.message : 'Unknown error'
      showErrorNotification('Deployment failed', errorMsg)
      console.error('Deploy error:', error)
    } finally {
      setIsLoading(false)
    }
  }

  return (
    <div className="w-full h-screen flex bg-white">
      {/* Left Sidebar - Component Palette */}
      <div className="w-64 border-r border-gray-200 overflow-hidden">
        <ComponentPalette onDragStart={() => {}} />
      </div>

      {/* Main Canvas Area */}
      <div className="flex-1 flex flex-col">
        {/* Deploy Button - Top Right */}
        <button
          onClick={deployPipeline}
          disabled={isLoading}
          className="fixed top-4 right-4 z-40 px-6 py-2 bg-red-600 hover:bg-red-700 disabled:bg-gray-400 text-white font-semibold rounded-lg transition-colors shadow-lg flex items-center gap-2"
        >
          {isLoading ? (
            <>
              <span className="animate-spin">⚙️</span>
              Deploying...
            </>
          ) : (
            <>🚀 Deploy</>
          )}
        </button>

        {/* ReactFlow Canvas */}
        <div
          ref={reactFlowWrapper}
          className="flex-1 w-full h-full bg-gradient-to-br from-slate-50 to-white"
          onDragOver={handleDragOver}
          onDrop={handleDrop}
        >
          <ReactFlow
            nodes={nodes}
            edges={edges}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onNodeClick={onNodeClick}
            nodeTypes={nodeTypes}
            fitView
          >
            <Background color="#e2e8f0" gap={16} />
            <Controls />
          </ReactFlow>
        </div>

        {/* Right-Slide Property Editor Panel */}
        {selectedNode && (
          <>
            {/* Semi-transparent overlay */}
            <div
              className="fixed inset-0 bg-black/10 z-30 cursor-pointer"
              onClick={() => setSelectedNode(null)}
            />

            {/* Property Editor Slide-In */}
            <div
              className={`
                fixed right-0 top-0 h-full w-96 bg-white shadow-2xl
                transition-transform duration-300 ease-out
                z-40 overflow-y-auto
              `}
            >
              <PropertyEditor
                node={selectedNode}
                onUpdate={updateNodeConfig}
                onClose={() => setSelectedNode(null)}
                onDelete={handleNodeDelete}
              />
            </div>
          </>
        )}
      </div>
    </div>
  )
}
