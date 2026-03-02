/**
 * Metrics Store
 * Manages real-time connection metrics
 */

import { create } from 'zustand'
import type { ConnectionMetrics } from '../types/models'

interface MetricsStore {
  metricsMap: Map<string, ConnectionMetrics>
  updateTimestamp: Record<string, number>

  // Actions
  updateMetrics: (connectionId: string, metrics: ConnectionMetrics) => void
  setMetrics: (metricsMap: Map<string, ConnectionMetrics>) => void
  clearMetrics: () => void
  removeMetrics: (connectionId: string) => void

  // Helpers
  getMetricsByConnectionId: (connectionId: string) => ConnectionMetrics | undefined
  getAllMetrics: () => ConnectionMetrics[]
  getLastUpdateTime: (connectionId: string) => number | undefined
}

export const useMetricsStore = create<MetricsStore>((set, get) => ({
  metricsMap: new Map(),
  updateTimestamp: {},

  updateMetrics: (connectionId, metrics) => {
    set((state) => {
      const newMap = new Map(state.metricsMap)
      newMap.set(connectionId, metrics)
      return {
        metricsMap: newMap,
        updateTimestamp: {
          ...state.updateTimestamp,
          [connectionId]: Date.now(),
        },
      }
    })
  },

  setMetrics: (metricsMap) => set({ metricsMap }),

  clearMetrics: () =>
    set({
      metricsMap: new Map(),
      updateTimestamp: {},
    }),

  removeMetrics: (connectionId) => {
    set((state) => {
      const newMap = new Map(state.metricsMap)
      newMap.delete(connectionId)
      const newTimestamps = { ...state.updateTimestamp }
      delete newTimestamps[connectionId]
      return {
        metricsMap: newMap,
        updateTimestamp: newTimestamps,
      }
    })
  },

  getMetricsByConnectionId: (connectionId) => {
    const { metricsMap } = get()
    return metricsMap.get(connectionId)
  },

  getAllMetrics: () => {
    const { metricsMap } = get()
    return Array.from(metricsMap.values())
  },

  getLastUpdateTime: (connectionId) => {
    const { updateTimestamp } = get()
    return updateTimestamp[connectionId]
  },
}))
