import { useState, useCallback, useRef, useMemo, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import KonvaCanvas from '../components/Pipeline/KonvaCanvas'
import PropertyEditor from '../components/Pipeline/PropertyEditor'
import ComponentPalette from '../components/Pipeline/ComponentPalette'
import CanvasSelector from '../components/CanvasSelector'
import apiClient from '../services/api'
import { useUIStore } from '../store/uiStore'
import { useAuthStore } from '../store/authStore'
import { useCanvasPersistence } from '../hooks/useCanvasPersistence'
import { getNodeLabel, renumberNodesAfterDeletion } from '../utils/nodeNumbering'
import { validatePipelineConnections, type ValidationResult } from '../utils/validation'
import { useNodeDrag } from '../hooks/useNodeDrag'
import { useConnectionDrawing } from '../hooks/useConnectionDrawing'
import type { Node, Edge } from '../types/pipeline'

export default function PipelineBuilder() {
  const navigate = useNavigate()
  const { user, isAuthenticated, logout } = useAuthStore()
  const [showUserMenu, setShowUserMenu] = useState(false)
  const userMenuRef = useRef<HTMLDivElement>(null)

  // Canvas persistence
  const {
    activeCanvas,
    canvases,
    currentCanvasId,
    isInitialized,
    canCreateMore,
    updateCanvas,
    forceUpdateCanvas,
    createCanvas,
    deleteCanvas,
    switchCanvas,
    renameCanvas,
  } = useCanvasPersistence()

  // Local state initialized from active canvas
  const [nodes, setNodes] = useState<Node[]>([])
  const [edges, setEdges] = useState<Edge[]>([])
  const [hasInitializedFromCanvas, setHasInitializedFromCanvas] = useState(false)
  const [selectedNode, setSelectedNode] = useState<Node | null>(null)
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
  const [selectedEdgeId, setSelectedEdgeId] = useState<string | null>(null)
  const [edgeContextMenu, setEdgeContextMenu] = useState<{ edgeId: string; x: number; y: number } | null>(null)
  const [isLoading, setIsLoading] = useState(false)
  const [paletteOpen, setPaletteOpen] = useState(true)
  const [deployAttempted, setDeployAttempted] = useState(false)
  const [canvasWidth, setCanvasWidth] = useState(window.innerWidth)
  const canvasContainer = useRef<HTMLDivElement>(null)
  const { showErrorNotification, showSuccessNotification } = useUIStore()

  // Close user menu when clicking outside
  useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      if (userMenuRef.current && !userMenuRef.current.contains(event.target as globalThis.Node)) {
        setShowUserMenu(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [])

  // Logout handler
  const handleLogout = async () => {
    setShowUserMenu(false)
    await logout()
    navigate('/login')
  }

  // Measure canvas container width and update when layout changes
  useEffect(() => {
    const updateCanvasWidth = () => {
      if (canvasContainer.current) {
        const containerWidth = canvasContainer.current.offsetWidth
        // Cap width to viewport minus left sidebar (64px) to prevent infinite expansion
        const maxWidth = window.innerWidth - 64
        setCanvasWidth(Math.min(containerWidth, maxWidth))
      }
    }

    // Initial measurement after DOM render
    updateCanvasWidth()

    // Update on window resize
    window.addEventListener('resize', updateCanvasWidth)

    // Use ResizeObserver to detect container size changes (e.g., sidebar open/close)
    const observer = new ResizeObserver(updateCanvasWidth)
    if (canvasContainer.current) {
      observer.observe(canvasContainer.current)
    }

    return () => {
      window.removeEventListener('resize', updateCanvasWidth)
      observer.disconnect()
    }
  }, [])

  // Initialize local state from active canvas when it becomes available
  useEffect(() => {
    if (isInitialized && activeCanvas && !hasInitializedFromCanvas) {
      setNodes(activeCanvas.nodes)
      setEdges(activeCanvas.edges)
      setHasInitializedFromCanvas(true)
    }
  }, [isInitialized, activeCanvas, hasInitializedFromCanvas])

  // When switching canvases, load the new canvas state
  useEffect(() => {
    if (isInitialized && activeCanvas && hasInitializedFromCanvas) {
      // Only update if we're switching to a different canvas
      setNodes(activeCanvas.nodes)
      setEdges(activeCanvas.edges)
      // Clear selection when switching canvases
      setSelectedNode(null)
      setSelectedNodeId(null)
      setSelectedEdgeId(null)
      setDeployAttempted(false)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentCanvasId]) // Intentionally only trigger on canvas ID change

  // Auto-save nodes/edges to canvas store (debounced in hook)
  useEffect(() => {
    if (isInitialized && hasInitializedFromCanvas) {
      updateCanvas(nodes, edges)
    }
  }, [nodes, edges, isInitialized, hasInitializedFromCanvas, updateCanvas])

  // Use custom hooks
  const { handleNodeDrag } = useNodeDrag(nodes, setNodes)
  const {
    connectionDrawing,
    connectionStart,
    connectionPreviewEnd,
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
      // Account for scroll position of the canvas container
      const scrollTop = canvasContainer.current.scrollTop || 0
      const x = event.clientX - rect.left
      const y = event.clientY - rect.top + scrollTop

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
    [nodes]
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

  // Edge selection handler
  const handleEdgeSelect = useCallback((edgeId: string) => {
    setSelectedEdgeId(edgeId || null)
    // Deselect node when selecting edge
    if (edgeId) {
      setSelectedNode(null)
      setSelectedNodeId(null)
    }
    // Close context menu when selecting different edge
    setEdgeContextMenu(null)
  }, [])

  // Edge deletion handler
  const handleEdgeDelete = useCallback((edgeId: string) => {
    setEdges((eds) => eds.filter((e) => e.id !== edgeId))
    setSelectedEdgeId(null)
    setEdgeContextMenu(null)
  }, [])

  // Edge context menu handler
  const handleEdgeContextMenu = useCallback((edgeId: string, x: number, y: number) => {
    setSelectedEdgeId(edgeId)
    setEdgeContextMenu({ edgeId, x, y })
  }, [])

  // Close context menu when clicking elsewhere
  const handleCloseContextMenu = useCallback(() => {
    setEdgeContextMenu(null)
  }, [])

  // Handle Delete key press
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Delete' || e.key === 'Backspace') {
        // Don't delete if user is typing in an input
        if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) {
          return
        }
        
        // Delete selected edge
        if (selectedEdgeId) {
          handleEdgeDelete(selectedEdgeId)
          e.preventDefault()
        }
      }
      
      // Close context menu on Escape
      if (e.key === 'Escape') {
        setEdgeContextMenu(null)
      }
    }

    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [selectedEdgeId, handleEdgeDelete])

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
    // Mark that user attempted to deploy (shows validation errors if any)
    setDeployAttempted(true)

    // First check graph validation (connectivity, cycles, orphans)
    if (!validationResult.valid) {
      return
    }

    // Then check node configurations
    if (!checkNodeConfigurations()) {
      return
    }

    const payload = buildConnectionPayload()
    setIsLoading(true)

    try {
      // Step 1: Create the connection
      const response = await apiClient.post('/api/v1/connections', payload)
      const connectionId = response.data?.data?.id
      
      if (!connectionId) {
        throw new Error('No connection ID returned from server')
      }

      // Step 2: Auto-start the pipeline
      try {
        await apiClient.post(`/api/v1/connections/${connectionId}/start`)
      } catch (startError) {
        console.error('Failed to auto-start pipeline:', startError)
        // Continue anyway - pipeline is created, just not started
      }

      // Step 3: Extract file path from producer node config for notification
      const producerNode = nodes.find(n => n.type === 'producer')
      let filePath = ''
      if (producerNode?.data?.config?.type === 'file') {
        filePath = (producerNode.data.config.file as any)?.path || ''
      }

      // Step 4: Show success notification with details
      const message = filePath 
        ? `Pipeline ${connectionId.substring(0, 8)}... deployed and running! Output: ${filePath}`
        : `Pipeline ${connectionId.substring(0, 8)}... deployed and running!`
      
      showSuccessNotification('Pipeline Started', message)
      
      // Keep canvas visible - just close property editor and reset deploy state
      setSelectedNode(null)
      setSelectedNodeId(null)
      setDeployAttempted(false)
      
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
    <div style={{ position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, display: 'flex', flexDirection: 'column' }}>
      {/* TOP HEADER BAR */}
      <header style={{ 
        height: '56px', 
        backgroundColor: '#ffffff', 
        borderBottom: '1px solid #e5e7eb', 
        display: 'flex', 
        alignItems: 'center', 
        justifyContent: 'space-between', 
        padding: '0 16px',
        flexShrink: 0,
        zIndex: 30,
      }}>
        {/* Left: Logo */}
        <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
          <div>
            <h1 style={{ margin: 0, fontSize: '18px', fontWeight: 700, color: '#111827' }}>VRSky</h1>
            <p style={{ margin: 0, fontSize: '11px', color: '#6b7280' }}>Integration Platform</p>
          </div>
        </div>

        {/* Right: Validation + Deploy + User Menu */}
        <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
          {/* Validation Errors Indicator */}
          {deployAttempted && !validationResult.valid && (
            <div
              style={{
                padding: '6px 12px',
                backgroundColor: '#fee2e2',
                color: '#991b1b',
                borderRadius: '4px',
                fontSize: '13px',
                fontWeight: 500,
                display: 'flex',
                alignItems: 'center',
                gap: '6px',
              }}
            >
              <span>✗</span>
              <span>{validationResult.errors.length} error{validationResult.errors.length > 1 ? 's' : ''}</span>
            </div>
          )}

          {/* Deploy Button */}
          <button
            onClick={deployPipeline}
            disabled={isLoading}
            style={{
              padding: '8px 24px',
              backgroundColor: isLoading ? '#9ca3af' : '#2563eb',
              color: 'white',
              fontWeight: 600,
              border: 'none',
              borderRadius: '4px',
              cursor: isLoading ? 'not-allowed' : 'pointer',
              fontSize: '14px',
            }}
          >
            {isLoading ? 'Deploying...' : 'Deploy'}
          </button>

          {/* User Menu */}
          <div style={{ position: 'relative' }} ref={userMenuRef}>
            <button
              onClick={() => setShowUserMenu(!showUserMenu)}
              style={{
                padding: '8px 12px',
                backgroundColor: 'transparent',
                border: '1px solid #e5e7eb',
                borderRadius: '6px',
                cursor: 'pointer',
                display: 'flex',
                alignItems: 'center',
                gap: '8px',
                fontSize: '14px',
                color: '#374151',
              }}
              title={isAuthenticated ? `Logged in as ${user?.email}` : 'Menu'}
            >
              <span style={{ fontSize: '16px' }}>⚙️</span>
              {isAuthenticated && user && (
                <span style={{ maxWidth: '120px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {user.full_name || user.email}
                </span>
              )}
            </button>

            {/* Dropdown Menu */}
            {showUserMenu && (
              <div style={{
                position: 'absolute',
                right: 0,
                top: '100%',
                marginTop: '8px',
                width: '220px',
                backgroundColor: '#ffffff',
                border: '1px solid #e5e7eb',
                borderRadius: '8px',
                boxShadow: '0 10px 15px -3px rgba(0, 0, 0, 0.1)',
                zIndex: 50,
              }}>
                {isAuthenticated && user ? (
                  <>
                    {/* User Info */}
                    <div style={{ padding: '12px 16px', borderBottom: '1px solid #e5e7eb' }}>
                      <p style={{ margin: 0, fontSize: '14px', fontWeight: 500, color: '#111827' }}>
                        {user.full_name || 'User'}
                      </p>
                      <p style={{ margin: '2px 0 0', fontSize: '12px', color: '#6b7280' }}>
                        {user.email}
                      </p>
                    </div>
                    {/* Menu Items */}
                    <div style={{ padding: '8px' }}>
                      {/* Settings Links */}
                      {[
                        { label: 'Connection Requests', path: '/settings/connection-requests' },
                        { label: 'Data Connections', path: '/settings/tenant-connections' },
                        { label: 'API Key', path: '/settings/api-key' },
                        { label: 'Audit Log', path: '/settings/audit-log' },
                      ].map(({ label, path }) => (
                        <button
                          key={path}
                          onClick={() => { setShowUserMenu(false); navigate(path) }}
                          style={{
                            width: '100%',
                            padding: '8px 12px',
                            backgroundColor: 'transparent',
                            border: 'none',
                            borderRadius: '4px',
                            cursor: 'pointer',
                            display: 'flex',
                            alignItems: 'center',
                            gap: '8px',
                            fontSize: '14px',
                            color: '#374151',
                            textAlign: 'left' as const,
                          }}
                          onMouseEnter={(e) => e.currentTarget.style.backgroundColor = '#f3f4f6'}
                          onMouseLeave={(e) => e.currentTarget.style.backgroundColor = 'transparent'}
                        >
                          {label}
                        </button>
                      ))}
                      <div style={{ borderTop: '1px solid #e5e7eb', margin: '4px 0' }} />
                      {/* Logout */}
                      <button
                        onClick={handleLogout}
                        style={{
                          width: '100%',
                          padding: '8px 12px',
                          backgroundColor: 'transparent',
                          border: 'none',
                          borderRadius: '4px',
                          cursor: 'pointer',
                          display: 'flex',
                          alignItems: 'center',
                          gap: '8px',
                          fontSize: '14px',
                          color: '#dc2626',
                          textAlign: 'left',
                        }}
                        onMouseEnter={(e) => e.currentTarget.style.backgroundColor = '#fef2f2'}
                        onMouseLeave={(e) => e.currentTarget.style.backgroundColor = 'transparent'}
                      >
                        <span>🚪</span>
                        <span>Logout</span>
                      </button>
                    </div>
                  </>
                ) : (
                  <div style={{ padding: '8px' }}>
                    <button
                      onClick={() => {
                        setShowUserMenu(false)
                        navigate('/login')
                      }}
                      style={{
                        width: '100%',
                        padding: '8px 12px',
                        backgroundColor: 'transparent',
                        border: 'none',
                        borderRadius: '4px',
                        cursor: 'pointer',
                        display: 'flex',
                        alignItems: 'center',
                        gap: '8px',
                        fontSize: '14px',
                        color: '#374151',
                        textAlign: 'left',
                      }}
                      onMouseEnter={(e) => e.currentTarget.style.backgroundColor = '#f3f4f6'}
                      onMouseLeave={(e) => e.currentTarget.style.backgroundColor = 'transparent'}
                    >
                      <span>🔑</span>
                      <span>Login</span>
                    </button>
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      </header>

      {/* MAIN CONTENT AREA - Sidebar + Canvas */}
      <div style={{ flex: 1, display: 'flex', flexDirection: 'row', overflow: 'hidden' }}>
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
        style={{ flex: 1, height: '100%', display: 'flex', flexDirection: 'column', backgroundColor: '#f9fafb' }}
      >
        {/* Canvas Selector Tabs */}
        <CanvasSelector
          canvases={canvases}
          currentCanvasId={currentCanvasId}
          canCreateMore={canCreateMore}
          onSwitch={switchCanvas}
          onCreate={createCanvas}
          onRename={renameCanvas}
          onDelete={deleteCanvas}
          onBeforeSwitch={() => forceUpdateCanvas(nodes, edges)}
        />

        {/* Canvas Content Area */}
        <div
          ref={canvasContainer}
          style={{ 
            flex: 1, 
            overflowY: 'auto', 
            overflowX: 'hidden', 
            position: 'relative',
            width: '100%',
            maxWidth: 'calc(100vw - 64px)', // Viewport width minus left sidebar (64px) - prevents infinite expansion
            paddingRight: rightSidebarOpen ? '320px' : '0px',
            transition: 'padding-right 150ms ease'
          }}
          onDragOver={handleDragOver}
          onDrop={handleDrop}
        >
          {/* Validation Errors Tooltip - shown below header when errors exist */}
          {deployAttempted && !validationResult.valid && (
            <div
              style={{
                position: 'absolute',
                top: '12px',
                right: '12px',
                zIndex: 20,
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

        {/* Konva Canvas - wrapped to prevent expansion */}
        <div style={{ width: '100%', height: '100%', overflow: 'hidden' }}>
          <KonvaCanvas
            nodes={nodes}
            edges={edges}
            selectedNodeId={selectedNodeId}
            selectedEdgeId={selectedEdgeId}
            connectionDrawing={connectionDrawing}
            connectionStart={connectionStart}
            connectionPreviewEnd={connectionPreviewEnd}
            containerWidth={canvasWidth}
            onNodeDrag={handleNodeDrag}
            onNodeSelect={(nodeId) => {
              // Set node ID for visual selection
              setSelectedNodeId(nodeId || null)
              // Deselect edge when selecting node
              setSelectedEdgeId(null)
              setEdgeContextMenu(null)
              // Find and set the selected node object
              if (nodeId) {
                const node = nodes.find((n) => n.id === nodeId)
                setSelectedNode(node || null)
              } else {
                setSelectedNode(null)
              }
            }}
            onEdgeSelect={handleEdgeSelect}
            onEdgeDelete={handleEdgeDelete}
            onEdgeContextMenu={handleEdgeContextMenu}
            onPortMouseDown={handlePortMouseDown}
            onPortMouseUp={handlePortMouseUp}
            onStageMouseMove={handleStageMouseMove}
          />
        </div>

        {/* Edge Context Menu */}
        {edgeContextMenu && (
          <div
            style={{
              position: 'absolute',
              left: edgeContextMenu.x,
              top: edgeContextMenu.y,
              backgroundColor: 'white',
              border: '1px solid #d1d5db',
              borderRadius: '6px',
              boxShadow: '0 4px 12px rgba(0, 0, 0, 0.15)',
              zIndex: 100,
              minWidth: '140px',
              overflow: 'hidden',
            }}
            onClick={(e) => e.stopPropagation()}
          >
            <button
              onClick={() => handleEdgeDelete(edgeContextMenu.edgeId)}
              style={{
                width: '100%',
                padding: '10px 16px',
                backgroundColor: 'white',
                border: 'none',
                fontSize: '13px',
                color: '#dc2626',
                cursor: 'pointer',
                textAlign: 'left',
                display: 'flex',
                alignItems: 'center',
                gap: '8px',
              }}
              onMouseEnter={(e) => (e.currentTarget.style.backgroundColor = '#fef2f2')}
              onMouseLeave={(e) => (e.currentTarget.style.backgroundColor = 'white')}
            >
              <span style={{ fontSize: '14px' }}>×</span>
              Delete Connection
            </button>
          </div>
        )}

        {/* Click overlay to close context menu */}
        {edgeContextMenu && (
          <div
            style={{
              position: 'absolute',
              top: 0,
              left: 0,
              right: 0,
              bottom: 0,
              zIndex: 99,
            }}
            onClick={handleCloseContextMenu}
          />
        )}
        </div>
      </div>
      </div>

      {/* RIGHT SIDEBAR - Property Editor (Fixed Overlay) */}
      {rightSidebarOpen && (
        <aside style={{ 
          position: 'fixed', 
          right: 0, 
          top: '56px', 
          width: '320px', 
          height: 'calc(100% - 56px)', 
          backgroundColor: 'white', 
          borderLeft: '1px solid #d1d5db', 
          overflowY: 'auto',
          zIndex: 40
        }}>
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
