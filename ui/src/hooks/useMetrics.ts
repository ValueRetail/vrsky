/**
 * useMetrics Hook
 * Polls the per-connection metrics endpoint (Prometheus-backed) on an interval
 * and feeds the metrics store. Replaces the old SSE stream, which had no
 * server-side data source (nothing published to the vrsky.metrics.* subjects it
 * listened on), so the dashboard always showed "No pipeline data available".
 */

import { useEffect } from 'react'
import { useMetricsStore } from '@/store/metricsStore'
import { metricsService } from '@/services/metricsService'
import type { ConnectionMetricsResponse } from '@/types/api'

interface UseMetricsOptions {
  enabled?: boolean
  onError?: (error: Error) => void
  /** Poll interval in ms. Default 5000. */
  intervalMs?: number
}

/**
 * Hook to poll per-connection metrics. Polls on mount and every intervalMs,
 * stops on unmount.
 * @param connectionId - The connection ID to fetch metrics for
 * @param options - Configuration options
 * @returns Current metrics for the connection (undefined until first load)
 */
export function useMetrics(connectionId: string, options: UseMetricsOptions = {}) {
  const { enabled = true, onError, intervalMs = 5000 } = options
  const { updateMetrics, getMetricsByConnectionId } = useMetricsStore()

  useEffect(() => {
    if (!enabled || !connectionId) {
      return
    }

    let cancelled = false

    const apply = (data: ConnectionMetricsResponse) => {
      if (cancelled) return
      updateMetrics(connectionId, {
        connection_id: data.connection_id,
        tenant_id: data.tenant_id,
        status: data.status,
        components: {
          consumer: data.components.consumer,
          converter: data.components.converter,
          filter: {
            ...data.components.filter,
            filtered_out: (data.components.filter as { filtered_out?: number }).filtered_out || 0,
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

    const poll = async () => {
      try {
        const data = await metricsService.getMetrics(connectionId)
        apply(data)
      } catch (err) {
        if (!cancelled && onError) onError(err as Error)
      }
    }

    poll()
    const timer = setInterval(poll, intervalMs)

    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [connectionId, enabled, intervalMs, updateMetrics, onError])

  return getMetricsByConnectionId(connectionId)
}
