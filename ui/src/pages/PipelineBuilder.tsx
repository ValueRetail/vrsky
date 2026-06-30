import { useState, useCallback, useRef, useMemo, useEffect } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { connectionService } from '../services/connectionService'
import { connectionToCanvas } from '../utils/connectionToCanvas'
import KonvaCanvas from '../components/Pipeline/KonvaCanvas'
import PropertyEditor from '../components/Pipeline/PropertyEditor'
import DLQPanel from '../components/Pipeline/DLQPanel'
import { listDLQ } from '../services/dlqService'
import { materializeSecrets } from '../utils/secrets'
import ComponentPalette from '../components/Pipeline/ComponentPalette'
import CanvasSelector from '../components/CanvasSelector'
import apiClient from '../services/api'
import * as authService from '../services/authService'
import { useUIStore } from '../store/uiStore'
import { useAuthStore } from '../store/authStore'
import { useCanvasPersistence } from '../hooks/useCanvasPersistence'
import TenantSelector from '../components/Tenants/TenantSelector'
import { getNodeLabel, renumberNodesAfterDeletion } from '../utils/nodeNumbering'
import { config } from '../config/env'
import { validatePipelineConnections, type ValidationResult } from '../utils/validation'
import { useNodeDrag } from '../hooks/useNodeDrag'
import { useConnectionDrawing } from '../hooks/useConnectionDrawing'
import type { Node, Edge } from '../types/pipeline'

// Auth header for the file-producer /files API. Empty unless a token is
// configured (see config.fileProducerToken), in which case the server requires it.
const fileProducerHeaders = (): HeadersInit =>
  config.fileProducerToken ? { Authorization: `Bearer ${config.fileProducerToken}` } : {}

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
    importCanvas,
    deleteCanvas,
    switchCanvas,
    renameCanvas,
    setDeployedConnectionId,
  } = useCanvasPersistence()

  // When routed to /connections/:id/edit, load that connection onto the canvas (#128).
  const { id: editConnectionId } = useParams<{ id: string }>()
  const [editLoadDone, setEditLoadDone] = useState(false)

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
  const [sidebarWidth, setSidebarWidth] = useState(320)
  const isResizing = useRef(false)
  const [deployAttempted, setDeployAttempted] = useState(false)
  const [canvasWidth, setCanvasWidth] = useState(window.innerWidth)
  const canvasContainer = useRef<HTMLDivElement>(null)
  const [fileUploadPanel, setFileUploadPanel] = useState<{ uploadUrl: string; watchDir: string; connectionId: string } | null>(null)
  const [fileUploading, setFileUploading] = useState(false)
  const [_fileUploadStatus, setFileUploadStatus] = useState<string | null>(null)
  const [fileEvents, setFileEvents] = useState<Array<{ type: string; filename?: string; size?: number; time: string; message?: string }>>([])
  const [httpProducerPanel, setHttpProducerPanel] = useState<{ url: string; connectionId: string } | null>(null)
  const [httpProducerEvents, setHttpProducerEvents] = useState<Array<{ type: string; message?: string; status_code?: number; time: string; payload?: string; response?: string }>>([])
  const [expandedEvent, setExpandedEvent] = useState<number | null>(null)
  const [dbProducerPanel, setDbProducerPanel] = useState<{ table: string; connectionId: string } | null>(null)
  const [dbProducerEvents, setDbProducerEvents] = useState<Array<{ type: string; message?: string; time: string; count?: number; payload?: string; table?: string; columns?: string[] }>>([])
  const [expandedDbEvent, setExpandedDbEvent] = useState<number | null>(null)
  const [deploymentInfo, setDeploymentInfo] = useState<{ connectionId: string; consumerType: string; consumerDetail: string; producerType: string; producerDetail: string; time: string } | null>(null)
  const [converterPanel, setConverterPanel] = useState<{ connectionId: string } | null>(null)
  const [converterEvents, setConverterEvents] = useState<Array<{ type: string; message?: string; time: string; before?: string; after?: string; fields?: number }>>([])
  const [expandedConverterEvent, setExpandedConverterEvent] = useState<number | null>(null)
  const [filterPanel, setFilterPanel] = useState<{ connectionId: string } | null>(null)
  const [filterEvents, setFilterEvents] = useState<Array<{ type: string; message?: string; time: string; data?: string; rules?: number }>>([])
  const [expandedFilterEvent, setExpandedFilterEvent] = useState<number | null>(null)
  const [fileManagerPanel, setFileManagerPanel] = useState<{ basePath: string; currentPath: string } | null>(null)
  const [fileManagerFiles, setFileManagerFiles] = useState<Array<{ name: string; path: string; isDir: boolean; size: number; modTime: string }>>([])
  const [fileManagerLoading, setFileManagerLoading] = useState(false)
  // Which bottom-panel tab is selected. Empty until the user picks one, in
  // which case the render falls back to the last available tab. Kept in state
  // (not derived from the DOM) so re-renders don't reset the user's choice.
  const [activeBottomTab, setActiveBottomTab] = useState<string>('')
  // Failed Messages count — drives whether the DLQ tab is shown (#74 feedback).
  const [dlqCount, setDlqCount] = useState(0)
  // (effect declared below after we know the active connection id)
  const { showErrorNotification, showSuccessNotification, showConfirmDialog, hideConfirmDialog } = useUIStore()

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

  // Sidebar resize via drag
  const handleResizeStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    isResizing.current = true
    const startX = e.clientX
    const startWidth = sidebarWidth

    const onMouseMove = (ev: MouseEvent) => {
      if (!isResizing.current) return
      const newWidth = Math.max(280, Math.min(800, startWidth + (startX - ev.clientX)))
      setSidebarWidth(newWidth)
    }
    const onMouseUp = () => {
      isResizing.current = false
      document.removeEventListener('mousemove', onMouseMove)
      document.removeEventListener('mouseup', onMouseUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
    document.addEventListener('mousemove', onMouseMove)
    document.addEventListener('mouseup', onMouseUp)
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
  }, [sidebarWidth])

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

  // Edit flow (#128): load a saved connection onto the canvas. Prefer an
  // existing local canvas already linked to this connection (positions intact);
  // otherwise rebuild the graph from the API with auto-layout. Switching/
  // importing changes currentCanvasId, which the canvas-switch effect above
  // picks up to load nodes/edges into local state.
  useEffect(() => {
    if (!editConnectionId || editLoadDone || !isInitialized) return
    let cancelled = false

    const linked = canvases.find((c) => c.deployedConnectionId === editConnectionId)
    if (linked) {
      switchCanvas(linked.id)
      setEditLoadDone(true)
      return
    }

    ;(async () => {
      try {
        const conn = await connectionService.get(editConnectionId)
        if (cancelled) return
        const { nodes: loadedNodes, edges: loadedEdges } = connectionToCanvas(conn)
        importCanvas(`Edit: ${conn.name || editConnectionId.slice(0, 8)}`, loadedNodes, loadedEdges, editConnectionId)
      } catch {
        if (!cancelled) {
          showErrorNotification('Edit connection', 'Failed to load this connection onto the canvas.')
        }
      } finally {
        if (!cancelled) setEditLoadDone(true)
      }
    })()

    return () => { cancelled = true }
  }, [editConnectionId, editLoadDone, isInitialized, canvases, switchCanvas, importCanvas, showErrorNotification])

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
        | 'input'
        | 'filter'
        | 'converter'
        | 'output'

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

    const { _label, ...restConfig } = config
    setNodes((nds) =>
      nds.map((node) =>
        node.id === selectedNode.id
          ? { ...node, data: { ...node.data, config: restConfig, ...(_label ? { label: _label as string } : {}) } }
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

  // SSE connection for file watcher events
  useEffect(() => {
    if (!fileUploadPanel) return
    setFileEvents([])
    const evtSource = new EventSource(`${config.fileConsumerUrl}/events/${fileUploadPanel.connectionId}`)
    evtSource.onmessage = (e) => {
      try {
        const event = JSON.parse(e.data)
        setFileEvents((prev) => [event, ...prev].slice(0, 50))
      } catch { /* ignore parse errors */ }
    }
    evtSource.onerror = () => {
      // Will auto-reconnect
    }
    return () => evtSource.close()
  }, [fileUploadPanel?.connectionId])

  // SSE connection for HTTP producer events
  useEffect(() => {
    if (!httpProducerPanel) return
    setHttpProducerEvents([])
    const evtSource = new EventSource(`${config.httpProducerUrl}/events/${httpProducerPanel.connectionId}`)
    evtSource.onmessage = (e) => {
      try {
        const event = JSON.parse(e.data)
        setHttpProducerEvents((prev) => [event, ...prev].slice(0, 50))
      } catch { /* ignore */ }
    }
    return () => evtSource.close()
  }, [httpProducerPanel?.connectionId])

  // SSE connection for DB producer events
  useEffect(() => {
    if (!dbProducerPanel) return
    setDbProducerEvents([])
    const evtSource = new EventSource(`${config.dbProducerUrl}/events/${dbProducerPanel.connectionId}`)
    evtSource.onmessage = (e) => {
      try {
        const event = JSON.parse(e.data)
        setDbProducerEvents((prev) => [event, ...prev].slice(0, 50))
      } catch { /* ignore */ }
    }
    return () => evtSource.close()
  }, [dbProducerPanel?.connectionId])

  // SSE connection for converter events
  useEffect(() => {
    if (!converterPanel) return
    setConverterEvents([])
    const evtSource = new EventSource(`${config.converterUrl}/events/${converterPanel.connectionId}`)
    evtSource.onmessage = (e) => {
      try {
        const event = JSON.parse(e.data)
        setConverterEvents((prev) => [event, ...prev].slice(0, 50))
      } catch { /* ignore */ }
    }
    return () => evtSource.close()
  }, [converterPanel?.connectionId])

  // SSE connection for filter events
  useEffect(() => {
    if (!filterPanel) return
    setFilterEvents([])
    const evtSource = new EventSource(`${config.filterUrl}/events/${filterPanel.connectionId}`)
    evtSource.onmessage = (e) => {
      try {
        const event = JSON.parse(e.data)
        setFilterEvents((prev) => [event, ...prev].slice(0, 50))
      } catch { /* ignore */ }
    }
    return () => evtSource.close()
  }, [filterPanel?.connectionId])

  // Validate pipeline on every change to nodes/edges
  const validationResult: ValidationResult = useMemo(() => {
    return validatePipelineConnections(nodes, edges)
  }, [nodes, edges])

  /**
   * Check if consumer and producer nodes have configuration.
   * This is separate from graph validation - ensures nodes are configured before deploy.
   */
  const checkNodeConfigurations = (): boolean => {
    const consumers = nodes.filter((n) => n.type === 'input')
    const producers = nodes.filter((n) => n.type === 'output')

    const consumerConfigured = consumers.some((n) => n.data.config && Object.keys(n.data.config).length > 0)
    const producerConfigured = producers.some((n) => n.data.config && Object.keys(n.data.config).length > 0)

    if (!consumerConfigured) {
      showErrorNotification('Pipeline validation', 'Configure Input node before deploying')
      return false
    }
    if (!producerConfigured) {
      showErrorNotification('Pipeline validation', 'Configure Output node before deploying')
      return false
    }

    return true
  }

  /**
   * Build the connection payload with nodes and edges in the new format.
   * This replaces the old source_config/destination_config format.
   *
   * Before persisting, plaintext credentials typed into the editor are minted
   * into encrypted tenant secrets and replaced with `<field>_secret_id`
   * references (see materializeSecrets) — so nothing plaintext is stored
   * server-side. The worker resolves the references back at runtime (#66).
   */
  const buildConnectionPayload = async () => {
    const builtNodes = await Promise.all(
      nodes.map(async (node) => ({
        id: node.id,
        type: node.type === 'input' ? 'consumer' : node.type === 'output' ? 'producer' : node.type,
        config: (await materializeSecrets(node.data.config || {}, node.id)) as Record<string, unknown>,
        enabled: true,
      }))
    )
    return {
      name: `Pipeline ${new Date().toLocaleTimeString()}`,
      description: 'Created via visual pipeline editor',
      nodes: builtNodes,
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

    let payload
    try {
      payload = await buildConnectionPayload()
    } catch (err) {
      showErrorNotification(
        'Secret storage',
        err instanceof Error ? err.message : 'Failed to store a credential securely'
      )
      return
    }
    setIsLoading(true)

    try {
      // Step 0: Determine connection ID — reuse existing or create new
      let connectionId: string
      const prevConnectionId = activeCanvas?.deployedConnectionId

      if (prevConnectionId) {
        // Redeploy: stop → update in place → restart (preserves webhook URL)
        try {
          await apiClient.post(`/api/v1/connections/${prevConnectionId}/stop`)
        } catch { /* may already be stopped */ }

        try {
          await apiClient.put(`/api/v1/connections/${prevConnectionId}`, payload)
          connectionId = prevConnectionId
        } catch {
          // Connection may have been deleted externally — fall back to create
          const response = await apiClient.post('/api/v1/connections', payload)
          connectionId = response.data?.data?.id
          if (!connectionId) throw new Error('No connection ID returned from server')
          if (currentCanvasId) setDeployedConnectionId(currentCanvasId, connectionId)
        }
      } else {
        // First deploy: create new connection
        const response = await apiClient.post('/api/v1/connections', payload)
        connectionId = response.data?.data?.id
        if (!connectionId) throw new Error('No connection ID returned from server')
        if (currentCanvasId) setDeployedConnectionId(currentCanvasId, connectionId)
      }

      // Step 1: Auto-start the pipeline
      try {
        await apiClient.post(`/api/v1/connections/${connectionId}/start`)
      } catch (startError) {
        console.error('Failed to auto-start pipeline:', startError)
      }

      // Step 3: Extract file path from producer node config for notification
      const producerNode = nodes.find(n => n.type === 'output')
      let filePath = ''
      if (producerNode?.data?.config?.type === 'file') {
        filePath = (producerNode.data.config.file as any)?.path || ''
      }

      // Step 4: Show success notification with details
      const consumerNode = nodes.find(n => n.type === 'input')
      const isWebhook = consumerNode?.data?.config?.type === 'http'
      const webhookUrl = isWebhook ? `${config.webhookIngressUrl}/webhook/${connectionId}` : ''

      let message = `Pipeline ${connectionId.substring(0, 8)}... deployed and running!`
      if (webhookUrl) {
        message += ` Webhook URL: ${webhookUrl}`
      } else if (filePath) {
        message += ` Output: ${filePath}`
      }

      showSuccessNotification('Pipeline Started', message)

      // Build deployment info summary
      const consumerType = (consumerNode?.data?.config?.type as string) || 'unknown'
      const producerType = (producerNode?.data?.config?.type as string) || 'unknown'
      let consumerDetail = ''
      let producerDetail = ''
      if (consumerType === 'file') consumerDetail = (consumerNode?.data?.config?.file as any)?.path || ''
      if (consumerType === 'http') consumerDetail = `${config.webhookIngressUrl}/webhook/${connectionId}`
      if (consumerType === 'database') {
        const dc = (consumerNode?.data?.config?.database as any) || {}
        consumerDetail = `${dc.host || ''}:${dc.port || 5432}/${dc.database || ''} → ${dc.table || dc.query || ''}`
      }
      if (producerType === 'file') producerDetail = (producerNode?.data?.config?.file as any)?.path || ''
      if (producerType === 'http') producerDetail = (producerNode?.data?.config?.http as any)?.url || ''
      if (producerType === 'database') {
        const dp = (producerNode?.data?.config?.database as any) || {}
        producerDetail = `${dp.host || ''}:${dp.port || 5432}/${dp.database || ''} → ${dp.table || ''}`
      }
      setDeploymentInfo({ connectionId, consumerType, consumerDetail, producerType, producerDetail, time: new Date().toLocaleTimeString() })

      // Show test panels based on consumer/producer type
      const isFileWatcher = consumerNode?.data?.config?.type === 'file'
      const isHttpProducer = producerNode?.data?.config?.type === 'http'
      const httpTargetUrl = isHttpProducer ? (producerNode?.data?.config?.http as any)?.url || '' : ''

      if (isFileWatcher) {
        setFileUploadPanel({
          uploadUrl: `${config.fileConsumerUrl}/upload/${connectionId}`,
          watchDir: `./data/input/${connectionId}`,
          connectionId,
        })
        setFileUploadStatus(null)
      } else {
        setFileUploadPanel(null)
      }

      // Show HTTP producer panel if applicable
      const payloadProducer = payload.nodes.find((n: { type: string; config?: Record<string, unknown> }) => n.type === 'output')
      const resolvedIsHttp = isHttpProducer || payloadProducer?.config?.type === 'http'
      const resolvedUrl = httpTargetUrl || (payloadProducer?.config?.http as any)?.url || ''

      if (resolvedIsHttp) {
        setHttpProducerEvents([])
        setExpandedEvent(null)
        setHttpProducerPanel({ url: resolvedUrl, connectionId })
        setDbProducerPanel(null)
        console.log('[deploy] HTTP producer panel set', { url: resolvedUrl, connectionId })
      } else {
        setHttpProducerPanel(null)
        console.log('[deploy] No HTTP producer found', { producerConfig: producerNode?.data?.config, payloadConfig: payloadProducer?.config })
      }

      // Show DB producer panel if applicable
      const isDbProducer = producerNode?.data?.config?.type === 'database' || payloadProducer?.config?.type === 'database'
      if (isDbProducer) {
        const dbTable = (producerNode?.data?.config?.database as any)?.table || (payloadProducer?.config?.database as any)?.table || ''
        setDbProducerEvents([])
        setExpandedDbEvent(null)
        setDbProducerPanel({ table: dbTable, connectionId })
        setHttpProducerPanel(null)
      } else {
        setDbProducerPanel(null)
      }

      // Show converter panel if pipeline has a converter node with config
      const converterNode = nodes.find(n => n.type === 'converter')
      const hasConverterConfig = converterNode?.data?.config && (
        ((converterNode.data.config.mappings as unknown[])?.length > 0) ||
        (converterNode.data.config.output_format as string)
      )
      if (hasConverterConfig) {
        setConverterEvents([])
        setExpandedConverterEvent(null)
        setConverterPanel({ connectionId })
      } else {
        setConverterPanel(null)
      }

      // Show filter panel if pipeline has a filter node with rules
      const filterNode = nodes.find(n => n.type === 'filter')
      const hasFilterRules = filterNode?.data?.config?.rules && (filterNode.data.config.rules as unknown[]).length > 0
      if (hasFilterRules) {
        setFilterEvents([])
        setExpandedFilterEvent(null)
        setFilterPanel({ connectionId })
      } else {
        setFilterPanel(null)
      }

      // Show file manager panel if producer is file type
      const isFileProducer = producerNode?.data?.config?.type === 'file' || payloadProducer?.config?.type === 'file'
      if (isFileProducer) {
        const outputPath = (producerNode?.data?.config?.file as any)?.path || (payloadProducer?.config?.file as any)?.path || '/data/output'
        setFileManagerPanel({ basePath: outputPath, currentPath: outputPath })
        setFileManagerFiles([])
        setActiveBottomTab('filemanager')
      } else {
        setFileManagerPanel(null)
      }

      // Keep canvas visible - just close property editor and reset deploy state
      setSelectedNode(null)
      setSelectedNodeId(null)
      setDeployAttempted(false)
      
    } catch (error) {
      // apiClient now rejects with VRSkyAPIError (extends Error), so the
      // server's `message` field (e.g. "integration count quota reached")
      // flows through naturally.
      const errorMsg = error instanceof Error ? error.message : 'Unknown error'
      showErrorNotification('Deployment failed', errorMsg)
      console.error('Deploy error:', error)
    } finally {
      setIsLoading(false)
    }
  }

  const uploadCountRef = { current: 0 }
  const handleFileUpload = async (file: File) => {
    if (!fileUploadPanel) return
    setFileUploading(true)
    try {
      const formData = new FormData()
      formData.append('file', file)
      const resp = await fetch(fileUploadPanel.uploadUrl, {
        method: 'POST',
        body: formData,
      })
      uploadCountRef.current++
      if (resp.ok) {
        setFileUploadStatus(`${uploadCountRef.current} file(s) uploaded — last: ${file.name}`)
      } else {
        setFileUploadStatus(`${resp.status} ${resp.statusText} — ${file.name}`)
      }
    } catch (err) {
      setFileUploadStatus(`Error: ${err instanceof Error ? err.message : 'Network error'}`)
    } finally {
      setFileUploading(false)
    }
  }

  // File manager helpers
  const fetchFileList = async (dirPath: string) => {
    setFileManagerLoading(true)
    try {
      const resp = await fetch(`${config.fileProducerUrl}/files?path=${encodeURIComponent(dirPath)}`, {
        headers: fileProducerHeaders(),
      })
      if (!resp.ok) {
        const data = await resp.json().catch(() => ({}))
        showErrorNotification('File manager', data.error || `Failed to load files (${resp.status})`)
        setFileManagerFiles([])
        return
      }
      const data = await resp.json()
      setFileManagerFiles(data.files || [])
      setFileManagerPanel(prev => prev ? { ...prev, currentPath: dirPath } : null)
    } catch (err) {
      showErrorNotification('File manager', err instanceof Error ? err.message : 'Network error')
      setFileManagerFiles([])
    } finally {
      setFileManagerLoading(false)
    }
  }

  const deleteFileOrDir = async (targetPath: string) => {
    const name = targetPath.split('/').pop() || targetPath
    if (!window.confirm(`Delete "${name}"? This cannot be undone.`)) return
    try {
      const resp = await fetch(`${config.fileProducerUrl}/files?path=${encodeURIComponent(targetPath)}`, {
        method: 'DELETE',
        headers: fileProducerHeaders(),
      })
      if (resp.ok) {
        showSuccessNotification('Deleted', `Deleted ${name}`)
        if (fileManagerPanel) fetchFileList(fileManagerPanel.currentPath)
      } else {
        const data = await resp.json().catch(() => ({}))
        showErrorNotification('Delete failed', data.error || `Delete failed (${resp.status})`)
      }
    } catch (err) {
      showErrorNotification('Delete failed', err instanceof Error ? err.message : 'Network error')
    }
  }

  // Poll DLQ for the active deployment so the "Failed Messages" tab can
  // hide itself when there's nothing to show (#74 user feedback).
  useEffect(() => {
    const connID = activeCanvas?.deployedConnectionId
    if (!connID) {
      setDlqCount(0)
      return
    }
    let cancelled = false
    const tick = async () => {
      try {
        const entries = await listDLQ(connID, 1, 0)
        if (!cancelled) setDlqCount(entries.length > 0 ? 1 : 0)
      } catch {
        if (!cancelled) setDlqCount(0)
      }
    }
    tick()
    const id = setInterval(tick, 15_000)
    return () => { cancelled = true; clearInterval(id) }
  }, [activeCanvas?.deployedConnectionId])

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

        {/* Center: Workspace Selector */}
        {isAuthenticated && <TenantSelector />}

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

          {/* Dashboard Button — single, clear entry point to the full
              sidebar navigation (connections, settings, usage, etc.). */}
          {isAuthenticated && (
            <button
              onClick={() => navigate('/connections')}
              style={{
                padding: '8px 16px',
                backgroundColor: 'transparent',
                color: '#374151',
                fontWeight: 600,
                border: '1px solid #e5e7eb',
                borderRadius: '6px',
                cursor: 'pointer',
                fontSize: '14px',
                display: 'flex',
                alignItems: 'center',
                gap: '6px',
              }}
              onMouseEnter={(e) => e.currentTarget.style.backgroundColor = '#f3f4f6'}
              onMouseLeave={(e) => e.currentTarget.style.backgroundColor = 'transparent'}
              title="Go to your dashboard"
            >
              <span>📊</span>
              <span>Dashboard</span>
            </button>
          )}

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
                    {/* Account actions only — all navigation lives in the
                        sidebar, reached via the Dashboard button. */}
                    <div style={{ padding: '8px' }}>
                      {/* Delete Account */}
                      <button
                        onClick={() => {
                          setShowUserMenu(false)
                          showConfirmDialog({
                            title: 'Delete Account',
                            message: 'This will permanently delete your account and all associated data. This action cannot be undone.',
                            confirmLabel: 'Delete Account',
                            destructive: true,
                            onConfirm: async () => {
                              hideConfirmDialog()
                              try {
                                await authService.deleteAccount()
                                await logout()
                                navigate('/login')
                              } catch {
                                // Token already cleared by deleteAccount on success
                              }
                            },
                          })
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
                          color: '#dc2626',
                          textAlign: 'left' as const,
                        }}
                        onMouseEnter={(e) => e.currentTarget.style.backgroundColor = '#fef2f2'}
                        onMouseLeave={(e) => e.currentTarget.style.backgroundColor = 'transparent'}
                      >
                        <span>Delete Account</span>
                      </button>
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
            paddingRight: rightSidebarOpen ? `${sidebarWidth}px` : '0px',
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

      {/* UNIFIED BOTTOM PANEL - Shows tabs for active panels */}
      {(() => {
        const tabs: Array<{ id: string; label: string; color: string }> = []
        if (fileUploadPanel) tabs.push({ id: 'files', label: 'File Watcher', color: '#16a34a' })
        if (converterPanel) tabs.push({ id: 'converter', label: 'Converter', color: '#d946ef' })
        if (filterPanel) tabs.push({ id: 'filter', label: 'Filter', color: '#f59e0b' })
        if (httpProducerPanel) tabs.push({ id: 'http', label: 'HTTP Output', color: '#7c3aed' })
        if (dbProducerPanel) tabs.push({ id: 'dbout', label: 'Database Output', color: '#ea580c' })
        if (fileManagerPanel) tabs.push({ id: 'filemanager', label: 'File Output', color: '#059669' })
        // Failed Messages tab — only when the deployed pipeline actually has
        // DLQ entries. Hides cleanly during normal operation (#74 feedback).
        if (activeCanvas?.deployedConnectionId && dlqCount > 0) {
          tabs.push({ id: 'dlq', label: 'Failed Messages', color: '#dc2626' })
        }
        if (tabs.length === 0) return null

        // Honour the user's selection if that tab still exists, otherwise fall
        // back to the last available tab (e.g. after a panel closes).
        const activeId = tabs.some(t => t.id === activeBottomTab)
          ? activeBottomTab
          : tabs[tabs.length - 1].id

        return (
          <div style={{
            position: 'fixed', bottom: 0,
            left: paletteOpen ? '224px' : '0px',
            right: rightSidebarOpen ? `${sidebarWidth}px` : '0px',
            height: '220px', backgroundColor: '#ffffff',
            borderTop: '2px solid #2563eb', zIndex: 35,
            display: 'flex', flexDirection: 'column',
            transition: 'left 150ms ease, right 150ms ease',
          }}>
            {/* Tab bar */}
            <div style={{
              display: 'flex', alignItems: 'center', borderBottom: '1px solid #e5e7eb',
              backgroundColor: '#f9fafb', flexShrink: 0,
            }}>
              {tabs.map((tab) => (
                <button
                  key={tab.id}
                  onClick={() => setActiveBottomTab(tab.id)}
                  className="bottom-tab-btn"
                  data-tab={tab.id}
                  style={{
                    padding: '6px 16px', fontSize: '12px', fontWeight: 600,
                    background: 'none', border: 'none',
                    borderBottom: tab.id === activeId ? `2px solid ${tab.color}` : '2px solid transparent',
                    color: tab.id === activeId ? tab.color : '#6b7280',
                    cursor: 'pointer',
                  }}
                >
                  {tab.label}
                </button>
              ))}
              <div style={{ flex: 1 }} />
              <button
                onClick={() => { setFileUploadPanel(null); setHttpProducerPanel(null); setDbProducerPanel(null); setConverterPanel(null); setFilterPanel(null); setFileManagerPanel(null); setDeploymentInfo(null) }}
                style={{ padding: '4px 12px', background: 'none', border: 'none', cursor: 'pointer', color: '#6b7280', fontSize: '16px' }}
              >×</button>
            </div>

            {/* Deployment info bar */}
            {deploymentInfo && (
              <div style={{
                display: 'flex', alignItems: 'center', gap: '16px', padding: '4px 16px',
                backgroundColor: '#f0fdf4', borderBottom: '1px solid #bbf7d0', fontSize: '11px', flexShrink: 0,
              }}>
                <span style={{ color: '#16a34a', fontWeight: 700 }}>● DEPLOYED</span>
                <span style={{ color: '#374151' }}>
                  <span style={{ color: '#6b7280' }}>Source:</span>{' '}
                  <span style={{ fontWeight: 600, textTransform: 'capitalize' }}>{deploymentInfo.consumerType}</span>
                  {deploymentInfo.consumerDetail && <code style={{ marginLeft: '4px', backgroundColor: '#e5e7eb', padding: '1px 4px', borderRadius: '3px', fontSize: '10px' }}>{deploymentInfo.consumerDetail}</code>}
                </span>
                <span style={{ color: '#d1d5db' }}>→</span>
                <span style={{ color: '#374151' }}>
                  <span style={{ color: '#6b7280' }}>Destination:</span>{' '}
                  <span style={{ fontWeight: 600, textTransform: 'capitalize' }}>{deploymentInfo.producerType}</span>
                  {deploymentInfo.producerDetail && <code style={{ marginLeft: '4px', backgroundColor: '#e5e7eb', padding: '1px 4px', borderRadius: '3px', fontSize: '10px' }}>{deploymentInfo.producerDetail}</code>}
                </span>
                <span style={{ color: '#9ca3af', marginLeft: 'auto' }}>ID: {deploymentInfo.connectionId.substring(0, 8)}… · {deploymentInfo.time}</span>
              </div>
            )}

            {/* Tab content */}
            <div id="bottom-panel-content" style={{ flex: 1, overflow: 'hidden' }}>
              {/* File watcher tab */}
              {fileUploadPanel && (
                <div className="bottom-tab-content" data-tab="files" style={{ display: activeId === 'files' ? 'flex' : 'none', height: '100%', padding: '8px 16px', gap: '10px' }}>
                  <label style={{ width: '180px', flexShrink: 0, border: '2px dashed #d1d5db', borderRadius: '6px', display: 'flex', alignItems: 'center', justifyContent: 'center', textAlign: 'center', cursor: fileUploading ? 'not-allowed' : 'pointer', backgroundColor: '#f9fafb', fontSize: '12px', color: '#6b7280', padding: '8px' }}
                    onDragOver={(e) => { e.preventDefault(); e.currentTarget.style.borderColor = '#16a34a'; e.currentTarget.style.backgroundColor = '#f0fdf4' }}
                    onDragLeave={(e) => { e.currentTarget.style.borderColor = '#d1d5db'; e.currentTarget.style.backgroundColor = '#f9fafb' }}
                    onDrop={async (e) => { e.preventDefault(); e.currentTarget.style.borderColor = '#d1d5db'; e.currentTarget.style.backgroundColor = '#f9fafb'; const items = e.dataTransfer.items; if (items) { const files: File[] = []; const readEntry = (entry: FileSystemEntry): Promise<void> => new Promise((resolve) => { if (entry.isFile) { (entry as FileSystemFileEntry).file((f) => { files.push(f); resolve() }) } else if (entry.isDirectory) { (entry as FileSystemDirectoryEntry).createReader().readEntries(async (entries) => { for (const ent of entries) await readEntry(ent); resolve() }) } else { resolve() } }); for (let i = 0; i < items.length; i++) { const entry = items[i].webkitGetAsEntry?.(); if (entry) await readEntry(entry) } for (const file of files) await handleFileUpload(file) } else { const file = e.dataTransfer.files[0]; if (file) handleFileUpload(file) } }}
                  >
                    <input type="file" multiple style={{ display: 'none' }} onChange={(e) => { const files = e.target.files; if (files) Array.from(files).forEach((f) => handleFileUpload(f)); e.target.value = '' }} disabled={fileUploading} />
                    <span>{fileUploading ? 'Uploading...' : 'Drop files here\nor click to browse'}</span>
                  </label>
                  <div style={{ flex: 1, overflowY: 'auto', fontSize: '12px', fontFamily: 'monospace', backgroundColor: '#f9fafb', border: '1px solid #e5e7eb', borderRadius: '4px', padding: '6px 8px' }}>
                    {fileEvents.length === 0 ? (
                      <div style={{ color: '#9ca3af', textAlign: 'center', padding: '12px' }}>Waiting for file activity...</div>
                    ) : fileEvents.map((evt, i) => (
                      <div key={i} style={{ padding: '2px 0', color: evt.type === 'error' ? '#dc2626' : '#374151' }}>
                        <span style={{ color: '#9ca3af' }}>{evt.time ? new Date(evt.time).toLocaleTimeString() : ''}</span>{' '}
                        <span style={{ fontWeight: 600, color: evt.type === 'added' ? '#2563eb' : evt.type === 'uploaded' ? '#7c3aed' : evt.type === 'deleted' ? '#ea580c' : evt.type === 'error' ? '#dc2626' : '#6b7280' }}>{evt.type.toUpperCase()}</span>{' '}
                        {evt.filename && <span>{evt.filename}</span>}
                        {evt.size != null && evt.size > 0 && <span style={{ color: '#9ca3af' }}> ({(evt.size / 1024).toFixed(1)} KB)</span>}
                        {evt.message && <span> — {evt.message}</span>}
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* Converter tab */}
              {converterPanel && (
                <div className="bottom-tab-content" data-tab="converter" style={{ display: activeId === 'converter' ? 'flex' : 'none', flexDirection: 'column', height: '100%', padding: '8px 16px' }}>
                  <div style={{ flex: 1, overflowY: 'auto', fontSize: '12px', fontFamily: 'monospace' }}>
                    {converterEvents.length === 0 ? (
                      <p style={{ color: '#9ca3af', fontStyle: 'italic', textAlign: 'center', marginTop: '12px' }}>Waiting for converter activity...</p>
                    ) : converterEvents.map((evt, i) => (
                      <div key={i} style={{ borderBottom: '1px solid #f3f4f6', paddingBottom: '4px', marginBottom: '4px' }}>
                        <div style={{ padding: '2px 0', cursor: (evt.before || evt.after) ? 'pointer' : 'default' }} onClick={() => (evt.before || evt.after) && setExpandedConverterEvent(expandedConverterEvent === i ? null : i)}>
                          <span style={{ color: '#9ca3af' }}>{evt.time ? new Date(evt.time).toLocaleTimeString() : ''}</span>{' '}
                          <span style={{ fontWeight: 600, color: evt.type === 'converted' ? '#16a34a' : evt.type === 'error' ? '#dc2626' : evt.type === 'info' ? '#2563eb' : '#6b7280' }}>
                            {evt.type === 'converted' ? '\u2713 CONVERTED' : evt.type === 'error' ? '\u2717 ERROR' : evt.type.toUpperCase()}
                          </span>{' '}
                          {evt.message && <span style={{ color: '#374151' }}>{evt.message}</span>}
                          {(evt.before || evt.after) && <span style={{ color: '#9ca3af', marginLeft: '8px' }}>{expandedConverterEvent === i ? '\u25BC' : '\u25B6'} details</span>}
                        </div>
                        {expandedConverterEvent === i && (
                          <div style={{ marginTop: '4px', display: 'flex', gap: '8px' }}>
                            {evt.before && (
                              <div style={{ flex: 1 }}>
                                <div style={{ fontSize: '10px', fontWeight: 600, color: '#6b7280', marginBottom: '2px' }}>BEFORE</div>
                                <pre style={{ backgroundColor: '#fef2f2', border: '1px solid #fecaca', borderRadius: '4px', padding: '6px', fontSize: '11px', color: '#111827', maxHeight: '100px', overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: 0 }}>{(() => { try { return JSON.stringify(JSON.parse(evt.before), null, 2) } catch { return evt.before } })()}</pre>
                              </div>
                            )}
                            {evt.after && (
                              <div style={{ flex: 1 }}>
                                <div style={{ fontSize: '10px', fontWeight: 600, color: '#6b7280', marginBottom: '2px' }}>AFTER</div>
                                <pre style={{ backgroundColor: '#f0fdf4', border: '1px solid #bbf7d0', borderRadius: '4px', padding: '6px', fontSize: '11px', color: '#111827', maxHeight: '100px', overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: 0 }}>{(() => { try { return JSON.stringify(JSON.parse(evt.after), null, 2) } catch { return evt.after } })()}</pre>
                              </div>
                            )}
                          </div>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* Filter tab */}
              {filterPanel && (
                <div className="bottom-tab-content" data-tab="filter" style={{ display: activeId === 'filter' ? 'flex' : 'none', flexDirection: 'column', height: '100%', padding: '8px 16px' }}>
                  <div style={{ flex: 1, overflowY: 'auto', fontSize: '12px', fontFamily: 'monospace' }}>
                    {filterEvents.length === 0 ? (
                      <p style={{ color: '#9ca3af', fontStyle: 'italic', textAlign: 'center', marginTop: '12px' }}>Waiting for filter activity...</p>
                    ) : filterEvents.map((evt, i) => (
                      <div key={i} style={{ borderBottom: '1px solid #f3f4f6', paddingBottom: '4px', marginBottom: '4px' }}>
                        <div style={{ padding: '2px 0', cursor: evt.data ? 'pointer' : 'default' }} onClick={() => evt.data && setExpandedFilterEvent(expandedFilterEvent === i ? null : i)}>
                          <span style={{ color: '#9ca3af' }}>{evt.time ? new Date(evt.time).toLocaleTimeString() : ''}</span>{' '}
                          <span style={{ fontWeight: 600, color: evt.type === 'passed' ? '#16a34a' : evt.type === 'dropped' ? '#ea580c' : evt.type === 'error' ? '#dc2626' : '#6b7280' }}>
                            {evt.type === 'passed' ? '\u2713 PASSED' : evt.type === 'dropped' ? '\u2717 DROPPED' : evt.type === 'error' ? '\u2717 ERROR' : evt.type.toUpperCase()}
                          </span>{' '}
                          {evt.message && <span style={{ color: '#374151' }}>{evt.message}</span>}
                          {evt.rules != null && evt.rules > 0 && <span style={{ color: '#9ca3af', marginLeft: '4px' }}>({evt.rules} rules)</span>}
                          {evt.data && <span style={{ color: '#9ca3af', marginLeft: '8px' }}>{expandedFilterEvent === i ? '\u25BC' : '\u25B6'} data</span>}
                        </div>
                        {expandedFilterEvent === i && evt.data && (
                          <pre style={{ backgroundColor: '#f9fafb', border: '1px solid #e5e7eb', borderRadius: '4px', padding: '6px', fontSize: '11px', maxHeight: '100px', overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: '4px 0 0 0' }}>{(() => { try { return JSON.stringify(JSON.parse(evt.data), null, 2) } catch { return evt.data } })()}</pre>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* HTTP producer tab */}
              {httpProducerPanel && (
                <div className="bottom-tab-content" data-tab="http" style={{ display: activeId === 'http' ? 'flex' : 'none', flexDirection: 'column', height: '100%', padding: '8px 16px' }}>
                  <div style={{ fontSize: '11px', color: '#6b7280', marginBottom: '4px' }}>
                    Target: <code style={{ backgroundColor: '#f3f4f6', padding: '1px 4px', borderRadius: '3px' }}>{httpProducerPanel.url}</code>
                  </div>
                  <div style={{ flex: 1, overflowY: 'auto', fontSize: '12px', fontFamily: 'monospace' }}>
                    {httpProducerEvents.length === 0 ? (
                      <p style={{ color: '#9ca3af', fontStyle: 'italic', textAlign: 'center', marginTop: '12px' }}>Waiting for HTTP activity...</p>
                    ) : httpProducerEvents.map((evt, i) => (
                      <div key={i} style={{ borderBottom: '1px solid #f3f4f6', paddingBottom: '4px', marginBottom: '4px' }}>
                        <div style={{ padding: '2px 0', cursor: (evt.payload || evt.response) ? 'pointer' : 'default' }} onClick={() => (evt.payload || evt.response) && setExpandedEvent(expandedEvent === i ? null : i)}>
                          <span style={{ color: '#9ca3af' }}>{evt.time ? new Date(evt.time).toLocaleTimeString() : ''}</span>{' '}
                          <span style={{ fontWeight: 600, color: evt.type === 'sent' ? '#16a34a' : evt.type === 'error' ? '#dc2626' : evt.type === 'info' ? '#2563eb' : '#6b7280' }}>
                            {evt.type === 'sent' ? '\u2713 SENT' : evt.type === 'error' ? '\u2717 ERROR' : evt.type.toUpperCase()}
                          </span>{' '}
                          {evt.message && <span style={{ color: '#374151' }}>{evt.message}</span>}
                          {(evt.payload || evt.response) && <span style={{ color: '#9ca3af', marginLeft: '8px' }}>{expandedEvent === i ? '\u25BC' : '\u25B6'} details</span>}
                        </div>
                        {expandedEvent === i && (
                          <div style={{ marginTop: '4px', display: 'flex', gap: '8px' }}>
                            {evt.payload && (
                              <div style={{ flex: 1 }}>
                                <div style={{ fontSize: '10px', fontWeight: 600, color: '#6b7280', marginBottom: '2px' }}>REQUEST BODY</div>
                                <pre style={{ backgroundColor: '#f9fafb', border: '1px solid #e5e7eb', borderRadius: '4px', padding: '6px', fontSize: '11px', color: '#111827', maxHeight: '100px', overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: 0 }}>{(() => { try { return JSON.stringify(JSON.parse(evt.payload), null, 2) } catch { return evt.payload } })()}</pre>
                              </div>
                            )}
                            {evt.response && (
                              <div style={{ flex: 1 }}>
                                <div style={{ fontSize: '10px', fontWeight: 600, color: '#6b7280', marginBottom: '2px' }}>RESPONSE</div>
                                <pre style={{ backgroundColor: '#f0fdf4', border: '1px solid #bbf7d0', borderRadius: '4px', padding: '6px', fontSize: '11px', color: '#111827', maxHeight: '100px', overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: 0 }}>{(() => { try { return JSON.stringify(JSON.parse(evt.response), null, 2) } catch { return evt.response } })()}</pre>
                              </div>
                            )}
                          </div>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* DB producer tab */}
              {dbProducerPanel && (
                <div className="bottom-tab-content" data-tab="dbout" style={{ display: activeId === 'dbout' ? 'flex' : 'none', flexDirection: 'column', height: '100%', padding: '8px 16px' }}>
                  <div style={{ fontSize: '11px', color: '#6b7280', marginBottom: '4px' }}>
                    Target table: <code style={{ backgroundColor: '#f3f4f6', padding: '1px 4px', borderRadius: '3px' }}>{dbProducerPanel.table || '(auto)'}</code>
                  </div>
                  <div style={{ flex: 1, overflowY: 'auto', fontSize: '12px', fontFamily: 'monospace' }}>
                    {dbProducerEvents.length === 0 ? (
                      <p style={{ color: '#9ca3af', fontStyle: 'italic', textAlign: 'center', marginTop: '12px' }}>Waiting for database write activity...</p>
                    ) : dbProducerEvents.map((evt, i) => (
                      <div key={i} style={{ borderBottom: '1px solid #f3f4f6', paddingBottom: '4px', marginBottom: '4px' }}>
                        <div style={{ padding: '2px 0', cursor: evt.payload ? 'pointer' : 'default' }} onClick={() => evt.payload && setExpandedDbEvent(expandedDbEvent === i ? null : i)}>
                          <span style={{ color: '#9ca3af' }}>{evt.time ? new Date(evt.time).toLocaleTimeString() : ''}</span>{' '}
                          <span style={{ fontWeight: 600, color: evt.type === 'inserted' ? '#16a34a' : evt.type === 'created' ? '#2563eb' : evt.type === 'error' ? '#dc2626' : '#6b7280' }}>
                            {evt.type === 'inserted' ? '\u2713 INSERTED' : evt.type === 'created' ? '\u2713 CREATED' : evt.type === 'error' ? '\u2717 ERROR' : evt.type.toUpperCase()}
                          </span>{' '}
                          {evt.message && <span style={{ color: '#374151' }}>{evt.message}</span>}
                          {evt.payload && <span style={{ color: '#9ca3af', marginLeft: '8px' }}>{expandedDbEvent === i ? '\u25BC' : '\u25B6'} details</span>}
                        </div>
                        {expandedDbEvent === i && evt.payload && (
                          <div style={{ marginTop: '4px', display: 'flex', gap: '8px' }}>
                            <div style={{ flex: 1 }}>
                              <div style={{ fontSize: '10px', fontWeight: 600, color: '#6b7280', marginBottom: '2px' }}>
                                DATA{evt.table ? ` → ${evt.table}` : ''}{evt.columns ? ` (${evt.columns.join(', ')})` : ''}
                              </div>
                              <pre style={{ backgroundColor: '#f9fafb', border: '1px solid #e5e7eb', borderRadius: '4px', padding: '6px', fontSize: '11px', color: '#111827', maxHeight: '120px', overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: 0 }}>{(() => { try { return JSON.stringify(JSON.parse(evt.payload), null, 2) } catch { return evt.payload } })()}</pre>
                            </div>
                          </div>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* File manager tab */}
              {fileManagerPanel && (
                <div className="bottom-tab-content" data-tab="filemanager" style={{ display: activeId === 'filemanager' ? 'flex' : 'none', flexDirection: 'column', height: '100%', padding: '8px 16px' }}>
                  {/* Breadcrumb + refresh */}
                  <div style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '6px', fontSize: '11px' }}>
                    <span style={{ color: '#6b7280' }}>Path:</span>
                    {(() => {
                      const parts = fileManagerPanel.currentPath.split('/').filter(Boolean)
                      return parts.map((part, i) => {
                        const fullPath = '/' + parts.slice(0, i + 1).join('/')
                        return (
                          <span key={fullPath}>
                            {i > 0 && <span style={{ color: '#d1d5db', margin: '0 2px' }}>/</span>}
                            <button
                              onClick={() => fetchFileList(fullPath)}
                              style={{ background: 'none', border: 'none', cursor: 'pointer', color: i === parts.length - 1 ? '#111827' : '#2563eb', fontWeight: i === parts.length - 1 ? 600 : 400, padding: 0, fontSize: '11px' }}
                            >{part}</button>
                          </span>
                        )
                      })
                    })()}
                    <button
                      onClick={() => fetchFileList(fileManagerPanel.currentPath)}
                      style={{ marginLeft: 'auto', padding: '2px 8px', fontSize: '11px', background: '#f3f4f6', border: '1px solid #d1d5db', borderRadius: '4px', cursor: 'pointer' }}
                    >Refresh</button>
                  </div>
                  {/* File listing */}
                  <div style={{ flex: 1, overflowY: 'auto', fontSize: '12px', fontFamily: 'monospace', backgroundColor: '#f9fafb', border: '1px solid #e5e7eb', borderRadius: '4px', padding: '4px' }}>
                    {fileManagerFiles.length === 0 && !fileManagerLoading ? (
                      <div style={{ color: '#9ca3af', textAlign: 'center', padding: '16px' }}>
                        {fileManagerPanel.currentPath ? 'No files yet. Deploy and trigger your pipeline to generate output.' : 'No path set.'}
                        <div style={{ marginTop: '8px' }}>
                          <button onClick={() => fetchFileList(fileManagerPanel.currentPath)} style={{ padding: '4px 12px', fontSize: '11px', background: '#2563eb', color: '#fff', border: 'none', borderRadius: '4px', cursor: 'pointer' }}>
                            Load Files
                          </button>
                        </div>
                      </div>
                    ) : fileManagerLoading ? (
                      <div style={{ color: '#9ca3af', textAlign: 'center', padding: '16px' }}>Loading...</div>
                    ) : (
                      <>
                        {/* Back button if not at base */}
                        {fileManagerPanel.currentPath !== fileManagerPanel.basePath && (
                          <div
                            onClick={() => {
                              const parent = fileManagerPanel.currentPath.substring(0, fileManagerPanel.currentPath.lastIndexOf('/')) || '/'
                              fetchFileList(parent)
                            }}
                            style={{ display: 'flex', alignItems: 'center', gap: '8px', padding: '4px 8px', cursor: 'pointer', borderBottom: '1px solid #e5e7eb', color: '#2563eb' }}
                          >
                            .. (up)
                          </div>
                        )}
                        {fileManagerFiles.map((file) => (
                          <div key={file.path} style={{ display: 'flex', alignItems: 'center', gap: '8px', padding: '4px 8px', borderBottom: '1px solid #f3f4f6' }}>
                            <span style={{ fontSize: '14px' }}>{file.isDir ? '\uD83D\uDCC1' : '\uD83D\uDCC4'}</span>
                            {file.isDir ? (
                              <button onClick={() => fetchFileList(file.path)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#2563eb', fontWeight: 600, padding: 0, fontSize: '12px', fontFamily: 'monospace' }}>{file.name}</button>
                            ) : (
                              <span style={{ color: '#374151' }}>{file.name}</span>
                            )}
                            <span style={{ color: '#9ca3af', fontSize: '10px', marginLeft: 'auto' }}>
                              {file.isDir ? '' : file.size < 1024 ? `${file.size} B` : `${(file.size / 1024).toFixed(1)} KB`}
                            </span>
                            <span style={{ color: '#9ca3af', fontSize: '10px' }}>{file.modTime ? new Date(file.modTime).toLocaleString() : ''}</span>
                            <button
                              onClick={() => deleteFileOrDir(file.path)}
                              style={{ padding: '1px 6px', fontSize: '10px', background: '#fef2f2', color: '#dc2626', border: '1px solid #fecaca', borderRadius: '3px', cursor: 'pointer' }}
                            >Delete</button>
                          </div>
                        ))}
                      </>
                    )}
                  </div>
                </div>
              )}

              {/* Failed Messages (DLQ) tab — Phase 1E / #70 */}
              {activeCanvas?.deployedConnectionId && (
                <div
                  className="bottom-tab-content"
                  data-tab="dlq"
                  style={{ display: activeId === 'dlq' ? 'flex' : 'none', flexDirection: 'column', height: '100%' }}
                >
                  <DLQPanel connectionID={activeCanvas.deployedConnectionId} />
                </div>
              )}
            </div>
          </div>
        )
      })()}

      {/* RIGHT SIDEBAR - Property Editor (Fixed Overlay) */}
      {rightSidebarOpen && (
        <aside style={{
          position: 'fixed',
          right: 0,
          top: '56px',
          width: `${sidebarWidth}px`,
          height: 'calc(100% - 56px)',
          backgroundColor: 'white',
          borderLeft: '1px solid #d1d5db',
          overflowY: 'auto',
          zIndex: 40
        }}>
          {/* Resize handle */}
          <div
            onMouseDown={handleResizeStart}
            style={{
              position: 'absolute',
              left: 0,
              top: 0,
              width: '4px',
              height: '100%',
              cursor: 'col-resize',
              zIndex: 50,
            }}
            onMouseEnter={(e) => { (e.target as HTMLElement).style.backgroundColor = '#3b82f6' }}
            onMouseLeave={(e) => { if (!isResizing.current) (e.target as HTMLElement).style.backgroundColor = 'transparent' }}
          />
          <PropertyEditor
            node={selectedNode}
            onUpdate={updateNodeConfig}
            onClose={handleClosePropertyEditor}
            onDelete={handleNodeDelete}
            deployedConnectionId={activeCanvas?.deployedConnectionId}
            allNodes={nodes}
            allEdges={edges}
          />
        </aside>
      )}
    </div>
  )
}
