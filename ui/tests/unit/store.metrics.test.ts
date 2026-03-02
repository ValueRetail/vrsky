/**
 * Metrics Store Tests
 */

import { describe, it, expect, beforeEach } from 'vitest'
import { useMetricsStore } from '@/store/metricsStore'
import type { ConnectionMetrics } from '@/types/models'

const mockMetrics: ConnectionMetrics = {
  connection_id: 'conn-1',
  tenant_id: 'tenant-1',
  status: 'running',
  components: {
    consumer: {
      status: 'active',
      messages_processed: 100,
      errors: 0,
      last_update: new Date().toISOString(),
    },
    converter: {
      status: 'active',
      messages_processed: 95,
      errors: 0,
      last_update: new Date().toISOString(),
    },
    filter: {
      status: 'active',
      messages_processed: 90,
      filtered_out: 5,
      errors: 0,
      last_update: new Date().toISOString(),
    },
    producer: {
      status: 'active',
      messages_processed: 90,
      messages_sent: 90,
      errors: 0,
      last_update: new Date().toISOString(),
    },
  },
  total_messages_in: 100,
  total_messages_out: 90,
  total_errors: 0,
  errors_per_second: 0,
  throughput_mps: 10.5,
  last_updated: new Date().toISOString(),
}

describe('Metrics Store', () => {
  beforeEach(() => {
    useMetricsStore.setState({
      metricsMap: new Map(),
      updateTimestamp: {},
    })
  })

  describe('Metrics Operations', () => {
    it('should update metrics for a connection', () => {
      const store = useMetricsStore.getState()
      store.updateMetrics('conn-1', mockMetrics)

      const state = useMetricsStore.getState()
      expect(state.metricsMap.size).toBe(1)
      expect(state.metricsMap.get('conn-1')).toEqual(mockMetrics)
    })

    it('should track update timestamp', () => {
      const store = useMetricsStore.getState()
      const before = Date.now()
      store.updateMetrics('conn-1', mockMetrics)
      const after = Date.now()

      const state = useMetricsStore.getState()
      const timestamp = state.updateTimestamp['conn-1']
      expect(timestamp).toBeDefined()
      expect(timestamp!).toBeGreaterThanOrEqual(before)
      expect(timestamp!).toBeLessThanOrEqual(after)
    })

    it('should get metrics by connection ID', () => {
      const store = useMetricsStore.getState()
      store.updateMetrics('conn-1', mockMetrics)

      const metrics = store.getMetricsByConnectionId('conn-1')
      expect(metrics).toEqual(mockMetrics)
    })

    it('should return undefined for non-existent metrics', () => {
      const store = useMetricsStore.getState()
      const metrics = store.getMetricsByConnectionId('non-existent')
      expect(metrics).toBeUndefined()
    })

    it('should remove metrics', () => {
      const store = useMetricsStore.getState()
      store.updateMetrics('conn-1', mockMetrics)
      store.removeMetrics('conn-1')

      const state = useMetricsStore.getState()
      expect(state.metricsMap.size).toBe(0)
      expect(state.updateTimestamp['conn-1']).toBeUndefined()
    })

    it('should clear all metrics', () => {
      const store = useMetricsStore.getState()
      store.updateMetrics('conn-1', mockMetrics)
      store.updateMetrics('conn-2', mockMetrics)

      store.clearMetrics()
      const state = useMetricsStore.getState()
      expect(state.metricsMap.size).toBe(0)
      expect(Object.keys(state.updateTimestamp)).toHaveLength(0)
    })
  })

  describe('Getters', () => {
    it('should get all metrics', () => {
      const store = useMetricsStore.getState()
      store.updateMetrics('conn-1', mockMetrics)
      store.updateMetrics('conn-2', mockMetrics)

      const all = store.getAllMetrics()
      expect(all).toHaveLength(2)
    })

    it('should get last update time for a connection', () => {
      const store = useMetricsStore.getState()
      store.updateMetrics('conn-1', mockMetrics)

      const time = store.getLastUpdateTime('conn-1')
      expect(time).toBeDefined()
      expect(typeof time).toBe('number')
    })

    it('should return undefined for non-existent update time', () => {
      const store = useMetricsStore.getState()
      const time = store.getLastUpdateTime('non-existent')
      expect(time).toBeUndefined()
    })
  })

  describe('Set Metrics', () => {
    it('should set entire metrics map', () => {
      const metricsMap = new Map()
      metricsMap.set('conn-1', mockMetrics)
      metricsMap.set('conn-2', mockMetrics)

      const store = useMetricsStore.getState()
      store.setMetrics(metricsMap)

      const state = useMetricsStore.getState()
      expect(state.metricsMap.size).toBe(2)
    })
  })
})
