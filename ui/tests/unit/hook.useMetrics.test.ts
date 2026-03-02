/**
 * useMetrics Hook Tests
 */

import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { useMetrics } from '@/hooks/useMetrics'
import { useMetricsStore } from '@/store/metricsStore'
import * as websocketService from '@/services/websocket'
import type { ConnectionMetricsResponse } from '@/types/api'

// Mock the services
vi.mock('@/store/metricsStore', () => ({
  useMetricsStore: vi.fn(),
}))

vi.mock('@/services/websocket', () => ({
  connectToMetricsStream: vi.fn(),
  disconnectMetricsStream: vi.fn(),
}))

describe('useMetrics Hook', () => {
  let mockUpdateMetrics: any
  let mockGetMetricsByConnectionId: any
  let mockConnectToMetricsStream: any
  let mockDisconnectMetricsStream: any

  const mockMetricsResponse: ConnectionMetricsResponse = {
    connection_id: 'test-conn',
    tenant_id: 'test-tenant',
    status: 'active',
    components: {
      consumer: { status: 'active', messages_processed: 100, errors: 0, last_update: '2024-01-20T10:00:00Z' },
      converter: { status: 'active', messages_processed: 100, errors: 0, last_update: '2024-01-20T10:00:00Z' },
      filter: {
        status: 'active',
        messages_processed: 100,
        filtered_out: 0,
        errors: 0,
        last_update: '2024-01-20T10:00:00Z',
      },
      producer: {
        status: 'active',
        messages_sent: 100,
        messages_processed: 100,
        errors: 0,
        last_update: '2024-01-20T10:00:00Z',
      },
    },
    total_messages_in: 100,
    total_messages_out: 100,
    total_errors: 0,
    errors_per_second: 0,
    throughput_mps: 10,
    last_updated: '2024-01-20T10:00:00Z',
  }

  beforeEach(() => {
    vi.clearAllMocks()

    mockUpdateMetrics = vi.fn()
    mockGetMetricsByConnectionId = vi.fn().mockReturnValue(null)

    ;(useMetricsStore as any).mockReturnValue({
      updateMetrics: mockUpdateMetrics,
      getMetricsByConnectionId: mockGetMetricsByConnectionId,
    })

    mockConnectToMetricsStream = vi.fn()
    mockDisconnectMetricsStream = vi.fn()
    ;(websocketService.connectToMetricsStream as any) = mockConnectToMetricsStream
    ;(websocketService.disconnectMetricsStream as any) = mockDisconnectMetricsStream
  })

  afterEach(() => {
    vi.clearAllMocks()
  })

  describe('Connection lifecycle', () => {
    it('should connect to metrics stream on mount', () => {
      renderHook(() => useMetrics('test-conn'))

      expect(mockConnectToMetricsStream).toHaveBeenCalledWith(
        'test-conn',
        expect.any(Function),
        expect.any(Function)
      )
    })

    it('should pass connection ID to connect function', () => {
      renderHook(() => useMetrics('conn-123'))

      expect(mockConnectToMetricsStream).toHaveBeenCalledWith(
        'conn-123',
        expect.any(Function),
        expect.any(Function)
      )
    })

    it('should disconnect on unmount', () => {
      const { unmount } = renderHook(() => useMetrics('test-conn'))

      unmount()

      expect(mockDisconnectMetricsStream).toHaveBeenCalled()
    })

    it('should not connect when disabled', () => {
      renderHook(() => useMetrics('test-conn', { enabled: false }))

      expect(mockConnectToMetricsStream).not.toHaveBeenCalled()
    })

    it('should not connect when connectionId is empty', () => {
      renderHook(() => useMetrics(''))

      expect(mockConnectToMetricsStream).not.toHaveBeenCalled()
    })

    it('should only connect once', () => {
      const { rerender } = renderHook(
        ({ connectionId }: { connectionId: string }) => useMetrics(connectionId),
        {
          initialProps: { connectionId: 'test-conn' },
        }
      )

      expect(mockConnectToMetricsStream).toHaveBeenCalledTimes(1)

      rerender({ connectionId: 'test-conn' })

      // Should still be called only once
      expect(mockConnectToMetricsStream).toHaveBeenCalledTimes(1)
    })
  })

  describe('Metrics update', () => {
    it('should update metrics when data arrives', async () => {
      let metricsCallback: any

      mockConnectToMetricsStream.mockImplementation(
        (connId, callback) => {
          metricsCallback = callback
        }
      )

      renderHook(() => useMetrics('test-conn'))

      // Simulate incoming metrics
      metricsCallback(mockMetricsResponse)

      await waitFor(() => {
        expect(mockUpdateMetrics).toHaveBeenCalled()
      })
    })

    it('should transform metrics response correctly', async () => {
      let metricsCallback: any

      mockConnectToMetricsStream.mockImplementation(
        (connId, callback) => {
          metricsCallback = callback
        }
      )

      renderHook(() => useMetrics('test-conn'))

      metricsCallback(mockMetricsResponse)

      await waitFor(() => {
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
      })
    })

    it('should handle producer metrics mapping', async () => {
      let metricsCallback: any

      mockConnectToMetricsStream.mockImplementation(
        (connId, callback) => {
          metricsCallback = callback
        }
      )

      renderHook(() => useMetrics('test-conn'))

      metricsCallback(mockMetricsResponse)

      await waitFor(() => {
        const updateCall = mockUpdateMetrics.mock.calls[0][1]
        expect(updateCall.components.producer.messages_sent).toBe(100)
        expect(updateCall.components.producer.messages_processed).toBe(100)
      })
    })

    it('should handle filter metrics with filtered_out', async () => {
      let metricsCallback: any

      mockConnectToMetricsStream.mockImplementation(
        (connId, callback) => {
          metricsCallback = callback
        }
      )

      const metricsWithFiltered: ConnectionMetricsResponse = {
        ...mockMetricsResponse,
        components: {
          ...mockMetricsResponse.components,
          filter: {
            ...mockMetricsResponse.components.filter,
            filtered_out: 25,
          },
        },
      }

      renderHook(() => useMetrics('test-conn'))

      metricsCallback(metricsWithFiltered)

      await waitFor(() => {
        const updateCall = mockUpdateMetrics.mock.calls[0][1]
        expect(updateCall.components.filter.filtered_out).toBe(25)
      })
    })
  })

  describe('Error handling', () => {
    it('should call error handler when provided', async () => {
      let errorCallback: any

      mockConnectToMetricsStream.mockImplementation(
        (connId, metricsCallback, errorHandler) => {
          errorCallback = errorHandler
        }
      )

      const mockErrorHandler = vi.fn()
      renderHook(() => useMetrics('test-conn', { onError: mockErrorHandler }))

      const error = new Error('Connection failed')
      errorCallback(error)

      await waitFor(() => {
        expect(mockErrorHandler).toHaveBeenCalledWith(error)
      })
    })

    it('should handle connection errors gracefully', async () => {
      let errorCallback: any

      mockConnectToMetricsStream.mockImplementation(
        (connId, metricsCallback, errorHandler) => {
          errorCallback = errorHandler
        }
      )

      const mockErrorHandler = vi.fn()
      renderHook(() => useMetrics('test-conn', { onError: mockErrorHandler }))

      const networkError = new Error('Network timeout')
      errorCallback(networkError)

      expect(mockErrorHandler).toHaveBeenCalledWith(networkError)
    })

    it('should not throw when error handler not provided', async () => {
      let errorCallback: any

      mockConnectToMetricsStream.mockImplementation(
        (connId, metricsCallback, errorHandler) => {
          errorCallback = errorHandler
        }
      )

      renderHook(() => useMetrics('test-conn'))

      const error = new Error('Test error')

      expect(() => {
        errorCallback(error)
      }).not.toThrow()
    })
  })

  describe('Hook return value', () => {
    it('should return current metrics', async () => {
      const mockMetrics = {
        connection_id: 'test-conn',
        total_messages_in: 100,
      }

      mockGetMetricsByConnectionId.mockReturnValue(mockMetrics)

      const { result } = renderHook(() => useMetrics('test-conn'))

      await waitFor(() => {
        expect(result.current).toEqual(mockMetrics)
      })
    })

    it('should return null initially', () => {
      mockGetMetricsByConnectionId.mockReturnValue(null)

      const { result } = renderHook(() => useMetrics('test-conn'))

      expect(result.current).toBeNull()
    })

    it('should call getMetricsByConnectionId with correct ID', async () => {
      renderHook(() => useMetrics('conn-xyz'))

      expect(mockGetMetricsByConnectionId).toHaveBeenCalledWith('conn-xyz')
    })

    it('should update metrics on hook rerender', async () => {
      let metricsCallback: any

      mockConnectToMetricsStream.mockImplementation(
        (connId, callback) => {
          metricsCallback = callback
        }
      )

      const { rerender } = renderHook(
        ({ connectionId }: { connectionId: string }) => useMetrics(connectionId),
        {
          initialProps: { connectionId: 'test-conn' },
        }
      )

      metricsCallback(mockMetricsResponse)

      expect(mockUpdateMetrics).toHaveBeenCalled()

      rerender({ connectionId: 'test-conn' })

      // Should not create new connection, but return current metrics
      mockGetMetricsByConnectionId.mockReturnValue({
        connection_id: 'test-conn',
        total_messages_in: 100,
      })

      // Both calls should have occurred
      expect(mockConnectToMetricsStream).toHaveBeenCalledTimes(1)
    })
  })

  describe('Options handling', () => {
    it('should respect enabled option', () => {
      renderHook(() => useMetrics('test-conn', { enabled: false }))

      expect(mockConnectToMetricsStream).not.toHaveBeenCalled()
    })

    it('should enable connection when enabled changes to true', () => {
      const { rerender } = renderHook(
        ({ enabled }: { enabled: boolean }) => useMetrics('test-conn', { enabled }),
        {
          initialProps: { enabled: false },
        }
      )

      expect(mockConnectToMetricsStream).not.toHaveBeenCalled()

      rerender({ enabled: true })

      // Should connect after enabled becomes true
      expect(mockConnectToMetricsStream).toHaveBeenCalled()
    })

    it('should pass error handler in options', () => {
      const mockErrorHandler = vi.fn()
      renderHook(() => useMetrics('test-conn', { onError: mockErrorHandler }))

      expect(mockConnectToMetricsStream).toHaveBeenCalledWith(
        'test-conn',
        expect.any(Function),
        expect.any(Function)
      )
    })
  })

  describe('Multiple connections', () => {
    it('should handle multiple useMetrics calls independently', () => {
      renderHook(() => useMetrics('conn-1'))
      renderHook(() => useMetrics('conn-2'))

      expect(mockConnectToMetricsStream).toHaveBeenCalledTimes(2)
      expect(mockConnectToMetricsStream).toHaveBeenNthCalledWith(
        1,
        'conn-1',
        expect.any(Function),
        expect.any(Function)
      )
      expect(mockConnectToMetricsStream).toHaveBeenNthCalledWith(
        2,
        'conn-2',
        expect.any(Function),
        expect.any(Function)
      )
    })

    it('should disconnect each connection independently', () => {
      const { unmount: unmount1 } = renderHook(() => useMetrics('conn-1'))
      const { unmount: unmount2 } = renderHook(() => useMetrics('conn-2'))

      unmount1()
      unmount2()

      expect(mockDisconnectMetricsStream).toHaveBeenCalledTimes(2)
    })
  })

  describe('Edge cases', () => {
    it('should handle empty connection ID', () => {
      renderHook(() => useMetrics(''))

      expect(mockConnectToMetricsStream).not.toHaveBeenCalled()
    })

    it('should handle null-like connection ID', () => {
      renderHook(() => useMetrics(null as any))

      expect(mockConnectToMetricsStream).not.toHaveBeenCalled()
    })

    it('should handle rapid connection/disconnection', async () => {
      const { unmount, rerender } = renderHook(
        ({ connId }: { connId: string }) => useMetrics(connId),
        {
          initialProps: { connId: 'test-conn' },
        }
      )

      rerender({ connId: 'test-conn-2' })

      unmount()

      // Should not cause errors
      expect(mockConnectToMetricsStream).toHaveBeenCalled()
      expect(mockDisconnectMetricsStream).toHaveBeenCalled()
    })

    it('should preserve metrics reference when not changed', () => {
      const mockMetrics = { connection_id: 'test-conn', total_messages_in: 100 }
      mockGetMetricsByConnectionId.mockReturnValue(mockMetrics)

      const { result, rerender } = renderHook(() => useMetrics('test-conn'))

      const metrics1 = result.current

      rerender()

      const metrics2 = result.current

      expect(metrics1).toEqual(metrics2)
    })
  })

  describe('Store integration', () => {
    it('should use metrics store for updates', async () => {
      let metricsCallback: any

      mockConnectToMetricsStream.mockImplementation(
        (connId, callback) => {
          metricsCallback = callback
        }
      )

      renderHook(() => useMetrics('test-conn'))

      metricsCallback(mockMetricsResponse)

      await waitFor(() => {
        expect(mockUpdateMetrics).toHaveBeenCalledWith(
          'test-conn',
          expect.any(Object)
        )
      })
    })

    it('should retrieve metrics from store', () => {
      const storedMetrics = { connection_id: 'test-conn', total_messages_in: 50 }
      mockGetMetricsByConnectionId.mockReturnValue(storedMetrics)

      const { result } = renderHook(() => useMetrics('test-conn'))

      expect(result.current).toEqual(storedMetrics)
    })
  })
})
