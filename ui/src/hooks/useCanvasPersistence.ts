/**
 * useCanvasPersistence Hook
 * Provides canvas persistence functionality for PipelineBuilder
 * Handles initialization, auto-save, and debounced updates
 */

import { useEffect, useRef, useCallback } from 'react'
import { useCanvasStore } from '../store/canvasStore'
import { useAuthStore } from '../store/authStore'
import type { Node, Edge, Canvas } from '../types/pipeline'

const DEBOUNCE_MS = 2000 // Auto-save after 2 seconds of inactivity

interface UseCanvasPersistenceReturn {
  // State
  activeCanvas: Canvas | null
  canvases: Canvas[]
  currentCanvasId: string | null
  isInitialized: boolean
  canCreateMore: boolean

  // Actions
  updateCanvas: (nodes: Node[], edges: Edge[]) => void
  forceUpdateCanvas: (nodes: Node[], edges: Edge[]) => void
  createCanvas: (name?: string) => Canvas | null
  deleteCanvas: (id: string) => void
  switchCanvas: (id: string) => void
  renameCanvas: (id: string, newName: string) => void
}

export function useCanvasPersistence(): UseCanvasPersistenceReturn {
  const {
    canvases,
    currentCanvasId,
    isInitialized,
    initialize,
    createCanvas,
    updateCanvas: storeUpdateCanvas,
    deleteCanvas,
    switchCanvas,
    renameCanvas,
    getActiveCanvas,
    canCreateMore,
  } = useCanvasStore()

  const { currentTenant } = useAuthStore()
  const debounceTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Initialize store on mount and re-initialize when tenant changes
  useEffect(() => {
    initialize(currentTenant?.id)
  }, [initialize, currentTenant?.id])

  // Debounced update function
  const updateCanvas = useCallback(
    (nodes: Node[], edges: Edge[]) => {
      if (!currentCanvasId) return

      // Clear previous timer
      if (debounceTimerRef.current) {
        clearTimeout(debounceTimerRef.current)
      }

      // Set new debounced update
      debounceTimerRef.current = setTimeout(() => {
        storeUpdateCanvas(currentCanvasId, nodes, edges)
      }, DEBOUNCE_MS)
    },
    [currentCanvasId, storeUpdateCanvas]
  )

  // Cleanup debounce timer on unmount
  useEffect(() => {
    return () => {
      if (debounceTimerRef.current) {
        clearTimeout(debounceTimerRef.current)
      }
    }
  }, [])

  // Force immediate save (useful before switching canvases)
  const forceUpdateCanvas = useCallback(
    (nodes: Node[], edges: Edge[]) => {
      if (!currentCanvasId) return

      // Clear any pending debounced update
      if (debounceTimerRef.current) {
        clearTimeout(debounceTimerRef.current)
        debounceTimerRef.current = null
      }

      // Save immediately
      storeUpdateCanvas(currentCanvasId, nodes, edges)
    },
    [currentCanvasId, storeUpdateCanvas]
  )

  return {
    activeCanvas: getActiveCanvas(),
    canvases,
    currentCanvasId,
    isInitialized,
    canCreateMore: canCreateMore(),
    updateCanvas,
    forceUpdateCanvas,
    createCanvas,
    deleteCanvas,
    switchCanvas,
    renameCanvas,
  }
}
