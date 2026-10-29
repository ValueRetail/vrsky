/**
 * Connections Store
 * Manages connection CRUD state
 */

import { create } from 'zustand'
import type { Connection, ConnectionStatus } from '../types/models'
import { connectionService } from '../services/connectionService'

// The badge and the sidebar counts read at most this many connections; the
// count itself is the API's total.
const SUMMARY_LIMIT = 100

export type StatusCounts = Record<ConnectionStatus, number>
const STATUSES: ConnectionStatus[] = ['running', 'stopped', 'error']

// Each load() takes a ticket; only the latest one may write, so a slow
// answer for the previous workspace can't overwrite the current one.
let loadTicket = 0

interface ConnectionsStore {
  connections: Connection[]
  /** The workspace's connection count from the API (load). */
  total: number
  /** Per-status totals from the API, set by load only when the workspace has
   *  more connections than one summary page holds; null when the loaded
   *  items are the whole workspace and can be counted directly (#541). */
  statusCounts: StatusCounts | null
  selectedConnection: Connection | null
  loading: boolean
  error: string | null

  // Actions
  /** Fetches the current workspace's connections (the X-Tenant-ID header
   *  carries the workspace); errors land in `error`. */
  load: () => Promise<void>
  addConnection: (connection: Connection) => void
  updateConnection: (connection: Connection) => void
  deleteConnection: (id: string) => void
  setSelectedConnection: (connection: Connection | null) => void
  clear: () => void

  // Helpers
  getConnectionById: (id: string) => Connection | undefined
  getRunningConnections: () => Connection[]
  getStoppedConnections: () => Connection[]
  getErrorConnections: () => Connection[]
}

export const useConnectionsStore = create<ConnectionsStore>((set, get) => ({
  connections: [],
  total: 0,
  statusCounts: null,
  selectedConnection: null,
  loading: false,
  error: null,

  load: async () => {
    const ticket = ++loadTicket
    set({ loading: true })
    try {
      const response = await connectionService.list(1, SUMMARY_LIMIT)
      if (ticket !== loadTicket) return
      // The list's wire shape is the API's GetConnectionResponse; the store
      // holds the richer Connection, as the pages do.
      const connections = response.connections as unknown as Connection[]
      // A full page with more behind it is only a sample, so ask the API for
      // each status's total (limit=1: only the count is used). A smaller page
      // is the whole workspace and the sidebar counts it as it is (#541).
      let statusCounts: StatusCounts | null = null
      if (connections.length >= SUMMARY_LIMIT && response.total > connections.length) {
        const totals = await Promise.all(STATUSES.map((st) => connectionService.list(1, 1, st)))
        if (ticket !== loadTicket) return
        statusCounts = { running: totals[0].total, stopped: totals[1].total, error: totals[2].total }
      }
      set({ connections, total: response.total, statusCounts, error: null })
    } catch (err) {
      if (ticket !== loadTicket) return
      // Never keep showing what may be another workspace's connections.
      set({
        connections: [],
        total: 0,
        statusCounts: null,
        error: err instanceof Error ? err.message : 'Failed to load connections',
      })
    } finally {
      if (ticket === loadTicket) set({ loading: false })
    }
  },

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

  // Also drops any load still in flight, so its answer can't land after.
  clear: () => {
    loadTicket++
    set({
      connections: [],
      total: 0,
      statusCounts: null,
      selectedConnection: null,
      loading: false,
      error: null,
    })
  },

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
