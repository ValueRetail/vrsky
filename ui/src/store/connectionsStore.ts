/**
 * Connections Store
 * Manages connection CRUD state
 */

import { create } from 'zustand'
import type { Connection } from '../types/models'

interface ConnectionsStore {
  connections: Connection[]
  selectedConnection: Connection | null
  loading: boolean
  error: string | null

  // Actions
  setConnections: (connections: Connection[]) => void
  addConnection: (connection: Connection) => void
  updateConnection: (connection: Connection) => void
  deleteConnection: (id: string) => void
  setSelectedConnection: (connection: Connection | null) => void
  setLoading: (loading: boolean) => void
  setError: (error: string | null) => void
  clear: () => void

  // Helpers
  getConnectionById: (id: string) => Connection | undefined
  getRunningConnections: () => Connection[]
  getStoppedConnections: () => Connection[]
  getErrorConnections: () => Connection[]
}

export const useConnectionsStore = create<ConnectionsStore>((set, get) => ({
  connections: [],
  selectedConnection: null,
  loading: false,
  error: null,

  setConnections: (connections) => set({ connections }),

  addConnection: (connection) =>
    set((state) => ({
      connections: [connection, ...state.connections],
    })),

  updateConnection: (connection) =>
    set((state) => ({
      connections: state.connections.map((c) =>
        c.id === connection.id ? connection : c
      ),
      selectedConnection:
        state.selectedConnection?.id === connection.id
          ? connection
          : state.selectedConnection,
    })),

  deleteConnection: (id) =>
    set((state) => ({
      connections: state.connections.filter((c) => c.id !== id),
      selectedConnection:
        state.selectedConnection?.id === id ? null : state.selectedConnection,
    })),

  setSelectedConnection: (connection) => set({ selectedConnection: connection }),

  setLoading: (loading) => set({ loading }),

  setError: (error) => set({ error }),

  clear: () =>
    set({
      connections: [],
      selectedConnection: null,
      loading: false,
      error: null,
    }),

  getConnectionById: (id) => {
    const { connections } = get()
    return connections.find((c) => c.id === id)
  },

  getRunningConnections: () => {
    const { connections } = get()
    return connections.filter((c) => c.status === 'running')
  },

  getStoppedConnections: () => {
    const { connections } = get()
    return connections.filter((c) => c.status === 'stopped')
  },

  getErrorConnections: () => {
    const { connections } = get()
    return connections.filter((c) => c.status === 'error')
  },
}))
