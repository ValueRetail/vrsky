/**
 * Canvas Store
 * Manages tenant canvas persistence with localStorage sync
 * Supports multiple named tenant canvases (max 10)
 */

import { create } from 'zustand'
import type { Canvas, Node, Edge } from '../types/pipeline'

const STORAGE_KEY_PREFIX = 'vrsky:canvases:v1'

const getStorageKey = (tenantId?: string): string => {
  return tenantId ? `${STORAGE_KEY_PREFIX}:${tenantId}` : STORAGE_KEY_PREFIX
}
const MAX_CANVASES = 10

// Helper to generate UUID
const generateId = (): string => {
  return crypto.randomUUID?.() || `canvas-${Date.now()}-${Math.random().toString(36).slice(2, 11)}`
}

// Helper to generate next tenant name (Tenant 1, Tenant 2, etc.)
const generateTenantName = (existingCanvases: Canvas[]): string => {
  const existingNumbers = existingCanvases
    .map((c) => {
      const match = c.name.match(/^Tenant (\d+)$/)
      return match ? parseInt(match[1], 10) : 0
    })
    .filter((n) => n > 0)

  let nextNumber = 1
  while (existingNumbers.includes(nextNumber)) {
    nextNumber++
  }
  return `Tenant ${nextNumber}`
}

// Helper to load from localStorage
const loadFromStorage = (tenantId?: string): { canvases: Canvas[]; currentCanvasId: string | null } => {
  try {
    const stored = localStorage.getItem(getStorageKey(tenantId))
    if (stored) {
      const data = JSON.parse(stored)
      return {
        canvases: data.canvases || [],
        currentCanvasId: data.currentCanvasId || null,
      }
    }
  } catch (error) {
    console.warn('Failed to load canvases from localStorage:', error)
  }
  return { canvases: [], currentCanvasId: null }
}

// Helper to save to localStorage
const saveToStorage = (canvases: Canvas[], currentCanvasId: string | null, tenantId?: string): void => {
  try {
    localStorage.setItem(getStorageKey(tenantId), JSON.stringify({ canvases, currentCanvasId }))
  } catch (error) {
    console.warn('Failed to save canvases to localStorage:', error)
  }
}

interface CanvasStore {
  canvases: Canvas[]
  currentCanvasId: string | null
  isInitialized: boolean
  tenantId: string | null

  // Actions
  initialize: (tenantId?: string) => void
  createCanvas: (name?: string) => Canvas | null
  updateCanvas: (id: string, nodes: Node[], edges: Edge[]) => void
  deleteCanvas: (id: string) => void
  switchCanvas: (id: string) => void
  renameCanvas: (id: string, newName: string) => void

  // Helpers
  getActiveCanvas: () => Canvas | null
  getCanvasById: (id: string) => Canvas | undefined
  canCreateMore: () => boolean
}

export const useCanvasStore = create<CanvasStore>((set, get) => ({
  canvases: [],
  currentCanvasId: null,
  isInitialized: false,
  tenantId: null,

  initialize: (tenantId?: string) => {
    const { isInitialized, tenantId: currentTenantId } = get()
    // Re-initialize if tenant changed
    if (isInitialized && tenantId === currentTenantId) return

    const { canvases, currentCanvasId } = loadFromStorage(tenantId)

    // If no canvases exist, create a default one
    if (canvases.length === 0) {
      const now = Date.now()
      const defaultCanvas: Canvas = {
        id: generateId(),
        name: 'Tenant 1',
        nodes: [],
        edges: [],
        createdAt: now,
        updatedAt: now,
      }
      set({
        canvases: [defaultCanvas],
        currentCanvasId: defaultCanvas.id,
        isInitialized: true,
        tenantId: tenantId || null,
      })
      saveToStorage([defaultCanvas], defaultCanvas.id, tenantId)
    } else {
      // Validate currentCanvasId exists, fallback to first canvas
      const validCurrentId = canvases.some((c) => c.id === currentCanvasId)
        ? currentCanvasId
        : canvases[0]?.id || null

      set({
        canvases,
        currentCanvasId: validCurrentId,
        isInitialized: true,
        tenantId: tenantId || null,
      })
    }
  },

  createCanvas: (name?: string) => {
    const { canvases, tenantId } = get()

    if (canvases.length >= MAX_CANVASES) {
      console.warn(`Cannot create more than ${MAX_CANVASES} canvases`)
      return null
    }

    const now = Date.now()
    const newCanvas: Canvas = {
      id: generateId(),
      name: name || generateTenantName(canvases),
      nodes: [],
      edges: [],
      createdAt: now,
      updatedAt: now,
    }

    const updatedCanvases = [...canvases, newCanvas]
    set({
      canvases: updatedCanvases,
      currentCanvasId: newCanvas.id,
    })
    saveToStorage(updatedCanvases, newCanvas.id, tenantId || undefined)

    return newCanvas
  },

  updateCanvas: (id, nodes, edges) => {
    const { canvases, currentCanvasId, tenantId } = get()
    const updatedCanvases = canvases.map((c) =>
      c.id === id
        ? { ...c, nodes, edges, updatedAt: Date.now() }
        : c
    )

    set({ canvases: updatedCanvases })
    saveToStorage(updatedCanvases, currentCanvasId, tenantId || undefined)
  },

  deleteCanvas: (id) => {
    const { canvases, currentCanvasId, tenantId } = get()

    // Don't allow deleting the last tenant canvas
    if (canvases.length <= 1) {
      console.warn('Cannot delete the last tenant')
      return
    }

    const updatedCanvases = canvases.filter((c) => c.id !== id)

    // If deleting the current canvas, switch to the first remaining one
    let newCurrentId = currentCanvasId
    if (currentCanvasId === id) {
      newCurrentId = updatedCanvases[0]?.id || null
    }

    set({
      canvases: updatedCanvases,
      currentCanvasId: newCurrentId,
    })
    saveToStorage(updatedCanvases, newCurrentId, tenantId || undefined)
  },

  switchCanvas: (id) => {
    const { canvases, currentCanvasId, tenantId } = get()
    const exists = canvases.some((c) => c.id === id)

    if (exists && id !== currentCanvasId) {
      set({ currentCanvasId: id })
      saveToStorage(canvases, id, tenantId || undefined)
    }
  },

  renameCanvas: (id, newName) => {
    const { canvases, currentCanvasId, tenantId } = get()
    const trimmedName = newName.trim()

    if (!trimmedName) {
      console.warn('Tenant name cannot be empty')
      return
    }

    const updatedCanvases = canvases.map((c) =>
      c.id === id
        ? { ...c, name: trimmedName, updatedAt: Date.now() }
        : c
    )

    set({ canvases: updatedCanvases })
    saveToStorage(updatedCanvases, currentCanvasId, tenantId || undefined)
  },

  getActiveCanvas: () => {
    const { canvases, currentCanvasId } = get()
    return canvases.find((c) => c.id === currentCanvasId) || null
  },

  getCanvasById: (id) => {
    const { canvases } = get()
    return canvases.find((c) => c.id === id)
  },

  canCreateMore: () => {
    const { canvases } = get()
    return canvases.length < MAX_CANVASES
  },
}))
