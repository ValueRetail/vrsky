/**
 * Canvas Store
 * Manages tenant canvas persistence with localStorage sync
 * Supports multiple named tenant canvases (max 10)
 */

import { create } from 'zustand'
import type { Canvas, Node, Edge } from '../types/pipeline'

const STORAGE_KEY = 'vrsky:canvases:v1'
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
const loadFromStorage = (): { canvases: Canvas[]; currentCanvasId: string | null } => {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
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
const saveToStorage = (canvases: Canvas[], currentCanvasId: string | null): void => {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ canvases, currentCanvasId }))
  } catch (error) {
    console.warn('Failed to save canvases to localStorage:', error)
  }
}

interface CanvasStore {
  canvases: Canvas[]
  currentCanvasId: string | null
  isInitialized: boolean

  // Actions
  initialize: () => void
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

  initialize: () => {
    const { isInitialized } = get()
    if (isInitialized) return

    const { canvases, currentCanvasId } = loadFromStorage()

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
      })
      saveToStorage([defaultCanvas], defaultCanvas.id)
    } else {
      // Validate currentCanvasId exists, fallback to first canvas
      const validCurrentId = canvases.some((c) => c.id === currentCanvasId)
        ? currentCanvasId
        : canvases[0]?.id || null

      set({
        canvases,
        currentCanvasId: validCurrentId,
        isInitialized: true,
      })
    }
  },

  createCanvas: (name?: string) => {
    const { canvases } = get()

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
    saveToStorage(updatedCanvases, newCanvas.id)

    return newCanvas
  },

  updateCanvas: (id, nodes, edges) => {
    const { canvases, currentCanvasId } = get()
    const updatedCanvases = canvases.map((c) =>
      c.id === id
        ? { ...c, nodes, edges, updatedAt: Date.now() }
        : c
    )

    set({ canvases: updatedCanvases })
    saveToStorage(updatedCanvases, currentCanvasId)
  },

  deleteCanvas: (id) => {
    const { canvases, currentCanvasId } = get()

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
    saveToStorage(updatedCanvases, newCurrentId)
  },

  switchCanvas: (id) => {
    const { canvases, currentCanvasId } = get()
    const exists = canvases.some((c) => c.id === id)

    if (exists && id !== currentCanvasId) {
      set({ currentCanvasId: id })
      saveToStorage(canvases, id)
    }
  },

  renameCanvas: (id, newName) => {
    const { canvases, currentCanvasId } = get()
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
    saveToStorage(updatedCanvases, currentCanvasId)
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
