/**
 * useMetrics Hook Tests
 *
 * The hook polls metricsService.getMetrics on an interval (Prometheus-backed)
 * and feeds the metrics store. (It previously used an SSE stream that had no
 * server-side data source.)
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { useMetrics } from '@/hooks/useMetrics'
import { useMetricsStore } from '@/store/metricsStore'
import { metricsService } from '@/services/metricsService'
import type { ConnectionMetricsResponse } from '@/types/api'

vi.mock('@/store/metricsStore', () => ({
  useMetricsStore: vi.fn(),
}))

vi.mock('@/services/metricsService', () => ({
  metricsService: {
    getMetrics: vi.fn(),
  },
}))

describe('useMetrics Hook', () => {
  let mockUpdateMetrics: ReturnType<typeof vi.fn>
  let mockGetMetricsByConnectionId: ReturnType<typeof vi.fn>
  const getMetrics = metricsService.getMetrics as unknown as ReturnType<typeof vi.fn>

  const mockMetricsResponse: ConnectionMetricsResponse = {
    connection_id: 'test-conn',
    tenant_id: 'test-tenant',
    status: 'active',
    components: {
      consumer: { status: 'active', messages_processed: 100, errors: 0, last_update: '2024-01-20T10:00:00Z' },
      converter: { status: 'active', messages_processed: 100, errors: 0, last_update: '2024-01-20T10:00:00Z' },
      filter: { status: 'active', messages_processed: 100, filtered_out: 0, errors: 0, last_update: '2024-01-20T10:00:00Z' },
      producer: { status: 'active', messages_sent: 100, messages_processed: 100, errors: 0, last_update: '2024-01-20T10:00:00Z' },
    },
    total_messages_in: 100,
    total_messages_out: 100,
    total_errors: 0,
    errors_per_second: 0,
    throughput_mps: 10,
    last_updated: '2024-01-20T10:00:00Z',
  } as ConnectionMetricsResponse

  beforeEach(() => {
    vi.clearAllMocks()
    mockUpdateMetrics = vi.fn()
    mockGetMetricsByConnectionId = vi.fn().mockReturnValue(null)
    ;(useMetricsStore as unknown as ReturnType<typeof vi.fn>).mockReturnValue({
      updateMetrics: mockUpdateMetrics,
      getMetricsByConnectionId: mockGetMetricsByConnectionId,
    })
    getMetrics.mockResolvedValue(mockMetricsResponse)
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.clearAllMocks()
  })

  describe('Polling lifecycle', () => {
    it('polls metrics on mount with the connection ID', async () => {
      renderHook(() => useMetrics('test-conn'))
      await waitFor(() => expect(getMetrics).toHaveBeenCalledWith('test-conn'))
    })

    it('does not poll when disabled', () => {
      renderHook(() => useMetrics('test-conn', { enabled: false }))
      expect(getMetrics).not.toHaveBeenCalled()
    })

    it('does not poll when connectionId is empty', () => {
      renderHook(() => useMetrics(''))
      expect(getMetrics).not.toHaveBeenCalled()
    })

    it('does not poll when connectionId is null-like', () => {
      renderHook(() => useMetrics(null as unknown as string))
      expect(getMetrics).not.toHaveBeenCalled()
    })

    it('polls again on the interval', async () => {
      vi.useFakeTimers()
      renderHook(() => useMetrics('test-conn', { intervalMs: 5000 }))
      // immediate poll on mount
      await vi.advanceTimersByTimeAsync(0)
      expect(getMetrics).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(5000)
      expect(getMetrics).toHaveBeenCalledTimes(2)
    })

    it('stops polling after unmount', async () => {
      vi.useFakeTimers()
      const { unmount } = renderHook(() => useMetrics('test-conn', { intervalMs: 5000 }))
      await vi.advanceTimersByTimeAsync(0)
      expect(getMetrics).toHaveBeenCalledTimes(1)
      unmount()
      await vi.advanceTimersByTimeAsync(10000)
      expect(getMetrics).toHaveBeenCalledTimes(1)
    })

    it('enables polling when enabled flips to true', async () => {
      const { rerender } = renderHook(
        ({ enabled }: { enabled: boolean }) => useMetrics('test-conn', { enabled }),
        { initialProps: { enabled: false } }
      )
      expect(getMetrics).not.toHaveBeenCalled()
      rerender({ enabled: true })
      await waitFor(() => expect(getMetrics).toHaveBeenCalledWith('test-conn'))
    })
  })

  describe('Metrics update + transform', () => {
    it('updates the store with the transformed totals', async () => {
      renderHook(() => useMetrics('test-conn'))
      await waitFor(() =>
        expect(mockUpdateMetrics).toHaveBeenCalledWith(
          'test-conn',
          expect.objectContaining({
            connection_id: 'test-conn',
            tenant_id: 'test-tenant',
            total_messages_in: 100,
            total_messages_out: 100,
            total_errors: 0,
            throughput_mps: 10,
          })
        )
      )
    })

    it('maps producer metrics', async () => {
      renderHook(() => useMetrics('test-conn'))
      await waitFor(() => expect(mockUpdateMetrics).toHaveBeenCalled())
      const arg = mockUpdateMetrics.mock.calls[0][1]
      expect(arg.components.producer.messages_sent).toBe(100)
      expect(arg.components.producer.messages_processed).toBe(100)
    })

    it('passes through filter filtered_out', async () => {
      getMetrics.mockResolvedValue({
        ...mockMetricsResponse,
        components: {
          ...mockMetricsResponse.components,
          filter: { ...mockMetricsResponse.components.filter, filtered_out: 25 },
        },
      })
      renderHook(() => useMetrics('test-conn'))
      await waitFor(() => expect(mockUpdateMetrics).toHaveBeenCalled())
      const arg = mockUpdateMetrics.mock.calls[0][1]
      expect(arg.components.filter.filtered_out).toBe(25)
    })
  })

  describe('Error handling', () => {
    it('calls onError when the request fails', async () => {
      const err = new Error('network')
      getMetrics.mockRejectedValue(err)
      const onError = vi.fn()
      renderHook(() => useMetrics('test-conn', { onError }))
      await waitFor(() => expect(onError).toHaveBeenCalledWith(err))
    })

    it('does not throw when onError is omitted and the request fails', async () => {
      getMetrics.mockRejectedValue(new Error('boom'))
      expect(() => renderHook(() => useMetrics('test-conn'))).not.toThrow()
      await waitFor(() => expect(getMetrics).toHaveBeenCalled())
    })
  })

  describe('Return value', () => {
    it('returns metrics from the store', () => {
      const stored = { connection_id: 'test-conn', total_messages_in: 50 }
      mockGetMetricsByConnectionId.mockReturnValue(stored)
      const { result } = renderHook(() => useMetrics('test-conn'))
      expect(result.current).toEqual(stored)
    })

    it('returns null before any data loads', () => {
      mockGetMetricsByConnectionId.mockReturnValue(null)
      const { result } = renderHook(() => useMetrics('test-conn'))
      expect(result.current).toBeNull()
    })

    it('reads the store with the correct connection ID', () => {
      renderHook(() => useMetrics('conn-xyz'))
      expect(mockGetMetricsByConnectionId).toHaveBeenCalledWith('conn-xyz')
    })
  })

  describe('Multiple connections', () => {
    it('polls each connection independently', async () => {
      renderHook(() => useMetrics('conn-1'))
      renderHook(() => useMetrics('conn-2'))
      await waitFor(() => {
        expect(getMetrics).toHaveBeenCalledWith('conn-1')
        expect(getMetrics).toHaveBeenCalledWith('conn-2')
      })
    })
  })
})
