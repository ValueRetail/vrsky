/**
 * useMetrics Hook
 * Manages SSE connection for real-time metrics streaming
 * Handles connection lifecycle, updates, and error management
 */

import { useEffect, useRef } from 'react'
import { useMetricsStore } from '@/store/metricsStore'
import { connectToMetricsStream, disconnectMetricsStream } from '@/services/websocket'
import type { ConnectionMetricsResponse } from '@/types/api'

interface UseMetricsOptions {
  enabled?: boolean
  onError?: (error: Error) => void
}

/**
 * Hook to manage SSE metrics connection for a connection ID
 * Automatically subscribes on mount and unsubscribes on unmount
 * @param connectionId - The connection ID to stream metrics for
 * @param options - Configuration options
 * @returns Current metrics for the connection (null if not yet loaded)
 */
export function useMetrics(connectionId: string, options: UseMetricsOptions = {}) {
  const { enabled = true, onError } = options
  const { updateMetrics, getMetricsByConnectionId } = useMetricsStore()
  const hasConnectedRef = useRef(false)

  useEffect(() => {
    if (!enabled || !connectionId || hasConnectedRef.current) {
      return
    }

    hasConnectedRef.current = true

    // Handler for incoming metrics
    const handleMetricsUpdate = (data: ConnectionMetricsResponse) => {
      updateMetrics(connectionId, {
        connection_id: data.connection_id,
        tenant_id: data.tenant_id,
        status: data.status,
        components: {
          consumer: data.components.consumer,
          converter: data.components.converter,
          filter: {
            ...data.components.filter,
            filtered_out: (data.components.filter as any).filtered_out || 0,
          },
          producer: {
            ...data.components.producer,
            messages_sent: data.components.producer.messages_sent,
            messages_processed: data.components.producer.messages_sent,
            status: data.components.producer.status,
            errors: data.components.producer.errors,
            last_update: data.components.producer.last_update,
          },
        },
        total_messages_in: data.total_messages_in,
        total_messages_out: data.total_messages_out,
        total_errors: data.total_errors,
        errors_per_second: data.errors_per_second,
        throughput_mps: data.throughput_mps,
        last_updated: data.last_updated,
      })
    }

    // Handler for connection errors
    const handleError = (error: Error) => {
      if (onError) {
        onError(error)
      }
    }

    // Connect to metrics stream
    connectToMetricsStream(connectionId, handleMetricsUpdate, handleError)

    // Cleanup: disconnect on unmount
    return () => {
      disconnectMetricsStream()
      hasConnectedRef.current = false
    }
  }, [connectionId, enabled, updateMetrics, onError])

  // Get current metrics for this connection
  const metrics = getMetricsByConnectionId(connectionId)

  return metrics
}
