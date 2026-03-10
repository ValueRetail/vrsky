import { useState, useCallback, useRef, useMemo } from 'react'
import KonvaCanvas from '../components/Pipeline/KonvaCanvas'
import PropertyEditor from '../components/Pipeline/PropertyEditor'
import ComponentPalette from '../components/Pipeline/ComponentPalette'
import apiClient from '../services/api'
import { useUIStore } from '../store/uiStore'
import { getNodeLabel, renumberNodesAfterDeletion } from '../utils/nodeNumbering'
import { validatePipelineConnections, type ValidationResult } from '../utils/validation'
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
  const [paletteOpen, setPaletteOpen] = useState(true)
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
    handleStageMouseMove,
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

      const rect = canvasContainer.current.getBoundingClientRect()
      const x = event.clientX - rect.left - stagePos.x
      const y = event.clientY - rect.top - stagePos.y

      const GRID_SIZE = 20
      const snappedX = Math.round(x / GRID_SIZE) * GRID_SIZE
      const snappedY = Math.round(y / GRID_SIZE) * GRID_SIZE

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

    setNodes((nds) => nds.filter((n) => n.id !== selectedNode.id))
    setEdges((eds) =>
      eds.filter((e) => e.source !== selectedNode.id && e.target !== selectedNode.id)
    )

    setNodes((nds) => renumberNodesAfterDeletion(nds, selectedNode.id))
    setSelectedNode(null)
    setSelectedNodeId(null)
  }, [selectedNode])

  const handleClosePropertyEditor = useCallback(() => {
    setSelectedNode(null)
    setSelectedNodeId(null)
  }, [])

  // Validate pipeline on every change to nodes/edges
  const validationResult: ValidationResult = useMemo(() => {
    return validatePipelineConnections(nodes, edges)
  }, [nodes, edges])

  /**
   * Check if consumer and producer nodes have configuration.
   * This is separate from graph validation - ensures nodes are configured before deploy.
   */
  const checkNodeConfigurations = (): boolean => {
    const consumers = nodes.filter((n) => n.type === 'consumer')
    const producers = nodes.filter((n) => n.type === 'producer')

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

  /**
   * Build the connection payload with nodes and edges in the new format.
   * This replaces the old source_config/destination_config format.
   */
  const buildConnectionPayload = () => {
    return {
      name: `Pipeline ${new Date().toLocaleTimeString()}`,
      description: 'Created via visual pipeline editor',
      nodes: nodes.map((node) => ({
        id: node.id,
        type: node.type,
        config: node.data.config || {},
        enabled: true,
      })),
      edges: edges.map((edge, index) => ({
        id: edge.id || `edge-${index}`,
        source: edge.source,
        target: edge.target,
        order: index,
      })),
    }
  }

  const deployPipeline = async () => {
    // First check graph validation (connectivity, cycles, orphans)
    if (!validationResult.valid) {
      showErrorNotification(
        'Pipeline Validation Failed',
        validationResult.errors.join('\n')
      )
      return
    }

    // Then check node configurations
    if (!checkNodeConfigurations()) {
      return
    }

    const payload = buildConnectionPayload()
    setIsLoading(true)

    try {
      await apiClient.post('/api/v1/connections', payload)
      showSuccessNotification('Success', 'Pipeline deployed successfully!')
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

  // Determine if right sidebar should be open
  const rightSidebarOpen = selectedNode !== null

  return (
    <div style={{ position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, display: 'flex', flexDirection: 'row' }}>
      {/* LEFT SIDEBAR - Component Palette */}
      {paletteOpen && (
        <aside style={{ width: '224px', height: '100%', backgroundColor: '#f9fafb', borderRight: '1px solid #e5e7eb', overflowY: 'auto', flexShrink: 0, display: 'flex', flexDirection: 'column' }}>
          <ComponentPalette onDragStart={() => {}} onClose={() => setPaletteOpen(false)} />
        </aside>
      )}

      {/* Toggle button to open sidebar when closed */}
      {!paletteOpen && (
        <button
          onClick={() => setPaletteOpen(true)}
          style={{ 
            position: 'absolute', 
            left: 0, 
            top: '50%', 
            transform: 'translateY(-50%)',
            width: '24px',
            height: '48px',
            backgroundColor: '#f3f4f6',
            border: '1px solid #e5e7eb',
            borderLeft: 'none',
            borderRadius: '0 6px 6px 0',
            cursor: 'pointer',
            zIndex: 20,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            color: '#6b7280',
            fontSize: '12px',
            fontWeight: 500,
          }}
          title="Open component palette"
        >
          {'>'}
        </button>
      )}

      {/* CENTER - Canvas Area (FILLS ALL REMAINING SPACE) */}
      <div
        ref={canvasContainer}
        style={{ flex: 1, height: '100%', overflow: 'hidden', position: 'relative', backgroundColor: '#f9fafb' }}
        onDragOver={handleDragOver}
        onDrop={handleDrop}
      >
        {/* Validation Status & Deploy Button - Top Right of Canvas */}
        <div
          style={{
            position: 'absolute',
            top: '12px',
            right: '12px',
            zIndex: 20,
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'flex-end',
            gap: '8px',
          }}
        >
          {/* Validation Status Badge */}
          {nodes.length > 0 && (
            <div
              style={{
                padding: '6px 12px',
                backgroundColor: validationResult.valid ? '#dcfce7' : '#fee2e2',
                color: validationResult.valid ? '#166534' : '#991b1b',
                borderRadius: '4px',
                fontSize: '13px',
                fontWeight: 500,
                display: 'flex',
                alignItems: 'center',
                gap: '6px',
                maxWidth: '280px',
              }}
            >
              <span>{validationResult.valid ? '✓' : '✗'}</span>
              <span>
                {validationResult.valid
                  ? 'Ready to Deploy'
                  : `${validationResult.errors.length} validation error${validationResult.errors.length > 1 ? 's' : ''}`}
              </span>
            </div>
          )}

          {/* Validation Errors List (collapsed by default, shown on hover or when there are errors) */}
          {!validationResult.valid && validationResult.errors.length > 0 && (
            <div
              style={{
                padding: '8px 12px',
                backgroundColor: '#fef2f2',
                border: '1px solid #fecaca',
                borderRadius: '4px',
                fontSize: '12px',
                color: '#991b1b',
                maxWidth: '280px',
              }}
            >
              <ul style={{ margin: 0, paddingLeft: '16px' }}>
                {validationResult.errors.map((error, index) => (
                  <li key={index} style={{ marginBottom: index < validationResult.errors.length - 1 ? '4px' : 0 }}>
                    {error}
                  </li>
                ))}
              </ul>
            </div>
          )}

          {/* Deploy Button */}
          <button
            onClick={deployPipeline}
            disabled={isLoading || !validationResult.valid}
            style={{
              padding: '8px 24px',
              backgroundColor: isLoading || !validationResult.valid ? '#9ca3af' : '#2563eb',
              color: 'white',
              fontWeight: 600,
              border: 'none',
              borderRadius: '4px',
              cursor: isLoading || !validationResult.valid ? 'not-allowed' : 'pointer',
            }}
            title={!validationResult.valid ? 'Fix validation errors before deploying' : ''}
          >
            {isLoading ? 'Deploying...' : 'Deploy'}
          </button>
        </div>

        {/* Konva Canvas */}
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
              setSelectedNode(null)
            }
          }}
          onPortMouseDown={handlePortMouseDown}
          onPortMouseUp={handlePortMouseUp}
          onStageDragMove={(x, y) => {
            setStagePos({ x, y })
            if (connectionDrawing) {
              setConnectionPreviewEnd({ x: -x, y: -y })
            }
          }}
          onStageMouseMove={handleStageMouseMove}
        />
      </div>

      {/* RIGHT SIDEBAR - Property Editor */}
      {rightSidebarOpen && (
        <aside style={{ width: '320px', height: '100%', backgroundColor: 'white', borderLeft: '1px solid #d1d5db', overflowY: 'auto', flexShrink: 0 }}>
          <PropertyEditor
            node={selectedNode}
            onUpdate={updateNodeConfig}
            onClose={handleClosePropertyEditor}
            onDelete={handleNodeDelete}
          />
        </aside>
      )}
    </div>
  )
}
