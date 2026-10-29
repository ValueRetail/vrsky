/**
 * The builder's edit flow (#128), moved out of PipelineBuilder so it can be
 * tested on its own (#502).
 *
 * Routed to /connections/:id/edit, the builder loads that connection onto the
 * canvas. It prefers a local canvas already linked to the connection
 * (positions intact); otherwise it rebuilds the graph from the API with
 * auto-layout. Switching or importing changes currentCanvasId, which the
 * page's canvas-switch effect picks up to load nodes and edges.
 */

import { useEffect, useRef, useState } from 'react'
import { connectionService } from '../services/connectionService'
import { connectionToCanvas } from '../utils/connectionToCanvas'
import { useCanvasStore } from '../store/canvasStore'
import { useUIStore } from '../store/uiStore'

export function useEditConnectionLoad(editConnectionId: string | undefined): void {
  const canvases = useCanvasStore((s) => s.canvases)
  const isInitialized = useCanvasStore((s) => s.isInitialized)
  const switchCanvas = useCanvasStore((s) => s.switchCanvas)
  const importCanvas = useCanvasStore((s) => s.importCanvas)
  const syncDeployedName = useCanvasStore((s) => s.syncDeployedName)
  const showErrorNotification = useUIStore((s) => s.showErrorNotification)

  const [editLoadDone, setEditLoadDone] = useState(false)
  // The already-linked canvas the edit flow switched to, whose name is then
  // refreshed from the server (#502).
  const [linkedEditCanvasId, setLinkedEditCanvasId] = useState<string | null>(null)
  // A canvas this flow just imported already has the server's name. The
  // import re-renders before its own pass finishes, so the next pass finds
  // it as "linked"; without this it would fetch the connection again.
  const importedCanvasId = useRef<string | null>(null)

  useEffect(() => {
    if (!editConnectionId || editLoadDone || !isInitialized) return
    let cancelled = false

    const linked = canvases.find((c) => c.deployedConnectionId === editConnectionId)
    if (linked) {
      switchCanvas(linked.id)
      if (linked.id !== importedCanvasId.current) setLinkedEditCanvasId(linked.id)
      setEditLoadDone(true)
      return
    }

    ;(async () => {
      try {
        const conn = await connectionService.get(editConnectionId)
        if (cancelled) return
        const { nodes: loadedNodes, edges: loadedEdges } = connectionToCanvas(conn)
        const imported = importCanvas(`Edit: ${conn.name || editConnectionId.slice(0, 8)}`, loadedNodes, loadedEdges, editConnectionId, conn.name)
        importedCanvasId.current = imported?.id ?? null
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

  // A linked canvas keeps its own graph, but the connection's name may have
  // changed elsewhere since it was deployed from here. Fetch the connection
  // and bring deployedName (and the tab, if the user never renamed it) in
  // line with the server, so Deploy's "was it renamed?" check compares
  // against the real name (#502). A failed fetch leaves both as they were;
  // the canvas is already open, and Deploy reports a deleted connection.
  useEffect(() => {
    if (!editConnectionId || !linkedEditCanvasId) return
    let cancelled = false
    connectionService.get(editConnectionId).then(
      (conn) => {
        if (!cancelled && conn.name) syncDeployedName(linkedEditCanvasId, editConnectionId, conn.name)
      },
      () => {},
    )
    return () => { cancelled = true }
  }, [editConnectionId, linkedEditCanvasId, syncDeployedName])
}
