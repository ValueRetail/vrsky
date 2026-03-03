import { useState, useCallback } from 'react'
import {
  ReactFlow,
  addEdge,
  useNodesState,
  useEdgesState,
  Background,
  Controls,
  MiniMap,
} from 'reactflow'
import type { Node, Connection } from 'reactflow'
import 'reactflow/dist/style.css'
import ConsumerNode from '../components/Pipeline/ConsumerNode'
import FilterNode from '../components/Pipeline/FilterNode'
import ConverterNode from '../components/Pipeline/ConverterNode'
import ProducerNode from '../components/Pipeline/ProducerNode'
import PropertyEditor from '../components/Pipeline/PropertyEditor'
import apiClient from '../services/api'
import { useUIStore } from '../store/uiStore'

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
  const [pipelineName, setPipelineName] = useState('New Pipeline')
  const [isLoading, setIsLoading] = useState(false)
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

  const addNode = (nodeType: 'consumer' | 'filter' | 'converter' | 'producer') => {
    const newNode: Node<NodeData> = {
      id: `${nodeType}-${Date.now()}`,
      type: nodeType,
      data: {
        label: nodeType.charAt(0).toUpperCase() + nodeType.slice(1),
        type: nodeType,
        config: {},
      },
      position: { x: Math.random() * 400, y: Math.random() * 400 },
    }
    setNodes((nds) => [...nds, newNode])
  }

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

  const validatePipeline = (): boolean => {
    // Must have exactly one consumer and one producer
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

    return true
  }

  const buildConnectionPayload = () => {
    // Extract consumer and producer from nodes
    const consumer = nodes.find((n) => n.type === 'consumer')
    const producer = nodes.find((n) => n.type === 'producer')

    if (!consumer?.data.config || !producer?.data.config) {
      showErrorNotification('Pipeline validation', 'Configure Consumer and Producer before deploying')
      return null
    }

    return {
      name: pipelineName,
      description: 'Created via visual pipeline editor',
      source_config: consumer.data.config,
      destination_config: producer.data.config,
      converter_config: {},
      filter_config: {},
    }
  }

  const deployPipeline = async () => {
    if (!validatePipeline()) return

    const payload = buildConnectionPayload()
    if (!payload) return

    setIsLoading(true)
    try {
      await apiClient.post('/api/v1/connections', payload)
      showSuccessNotification('Success', 'Pipeline deployed successfully!')
      // Reset
      setNodes([])
      setEdges([])
      setPipelineName('New Pipeline')
    } catch (error) {
      showErrorNotification('Deployment failed', 'Failed to deploy pipeline')
      console.error(error)
    } finally {
      setIsLoading(false)
    }
  }

  return (
    <div className="w-full h-screen flex flex-col bg-gradient-to-br from-slate-900 via-slate-800 to-slate-900">
      {/* Header */}
      <div className="bg-slate-950 border-b border-slate-700 p-4 flex items-center justify-between shadow-lg">
        <div className="flex-1">
          <input
            type="text"
            value={pipelineName}
            onChange={(e) => setPipelineName(e.target.value)}
            className="text-2xl font-bold bg-transparent border-0 border-b border-transparent hover:border-slate-600 focus:border-blue-400 outline-none text-white placeholder-slate-400"
            placeholder="Pipeline name"
          />
        </div>

        {/* Node Palette */}
        <div className="flex gap-2 mx-6">
          <button
            onClick={() => addNode('consumer')}
            className="px-4 py-2 bg-blue-600 text-white rounded-md hover:bg-blue-700 active:bg-blue-800 text-sm font-medium transition-colors shadow-md"
            title="Add data source (Consumer)"
          >
            + Consumer
          </button>
          <button
            onClick={() => addNode('filter')}
            className="px-4 py-2 bg-yellow-600 text-white rounded-md hover:bg-yellow-700 active:bg-yellow-800 text-sm font-medium transition-colors shadow-md"
            title="Add filtering logic (Filter)"
          >
            + Filter
          </button>
          <button
            onClick={() => addNode('converter')}
            className="px-4 py-2 bg-purple-600 text-white rounded-md hover:bg-purple-700 active:bg-purple-800 text-sm font-medium transition-colors shadow-md"
            title="Add data transformation (Converter)"
          >
            + Converter
          </button>
          <button
            onClick={() => addNode('producer')}
            className="px-4 py-2 bg-green-600 text-white rounded-md hover:bg-green-700 active:bg-green-800 text-sm font-medium transition-colors shadow-md"
            title="Add data destination (Producer)"
          >
            + Producer
          </button>
        </div>

        {/* Deploy Button */}
        <button
          onClick={deployPipeline}
          disabled={isLoading}
          className="px-5 py-2 bg-gradient-to-r from-indigo-600 to-indigo-500 text-white rounded-md hover:from-indigo-700 hover:to-indigo-600 disabled:from-slate-600 disabled:to-slate-500 font-medium transition-all shadow-lg hover:shadow-indigo-500/20 disabled:shadow-none"
        >
          {isLoading ? (
            <span className="flex items-center gap-2">
              <span className="animate-spin">⚡</span> Deploying...
            </span>
          ) : (
            '🚀 Deploy'
          )}
        </button>
      </div>

      {/* Canvas + Property Editor */}
      <div className="flex-1 flex overflow-hidden bg-slate-800">
        {/* Canvas */}
        <div className="flex-1 w-full h-full">
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
            <Background color="#334155" gap={16} />
            <Controls />
            <MiniMap 
              style={{ 
                backgroundColor: '#1e293b',
                border: '1px solid #475569'
              }} 
            />
          </ReactFlow>
        </div>

        {/* Property Editor */}
        {selectedNode && (
          <PropertyEditor
            node={selectedNode}
            onUpdate={updateNodeConfig}
            onClose={() => setSelectedNode(null)}
          />
        )}
      </div>
    </div>
  )
}
