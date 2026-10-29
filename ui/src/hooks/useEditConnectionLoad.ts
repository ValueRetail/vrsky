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

  // The connection id the flow last finished for. Kept per id, not as a flag,
  // so routing an open builder from /connections/A/edit to B/edit loads B
  // (#516).
  const [loadedForId, setLoadedForId] = useState<string | null>(null)
  const editLoadDone = loadedForId !== null && loadedForId === editConnectionId
  // The already-linked canvas the edit flow switched to, whose name is then
  // refreshed from the server (#502), with the connection it was found for.
  // A canvas found for A is never synced against B (#516).
  const [linkedEdit, setLinkedEdit] = useState<{ connectionId: string; canvasId: string } | null>(null)
  const linkedEditCanvasId =
    linkedEdit && linkedEdit.connectionId === editConnectionId ? linkedEdit.canvasId : null
  // A canvas this flow just imported already has the server's name. The
  // import's store update renders on its own, ahead of setLoadedForId, so the
  // next pass finds it as "linked"; without this it would fetch the
  // connection again. The next linked pass consumes it, so a later visit to
  // that connection (after the route went elsewhere and came back) syncs as
  // usual (#516).
  const importedCanvasId = useRef<string | null>(null)

  useEffect(() => {
    // Off the edit route (the plain builder at /) the page stays mounted, so
    // forget the last load: coming back to the same connection loads it again
    // instead of leaving the builder on whatever tab the user picked (#516).
    if (!editConnectionId) {
      setLoadedForId(null)
      setLinkedEdit(null)
      importedCanvasId.current = null
      return
    }
    if (editLoadDone || !isInitialized) return
    let cancelled = false

    const linked = canvases.find((c) => c.deployedConnectionId === editConnectionId)
    if (linked) {
      switchCanvas(linked.id)
      const justImported = linked.id === importedCanvasId.current
      importedCanvasId.current = null
      if (!justImported) setLinkedEdit({ connectionId: editConnectionId, canvasId: linked.id })
      setLoadedForId(editConnectionId)
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
        if (!cancelled) setLoadedForId(editConnectionId)
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
