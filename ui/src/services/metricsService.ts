/**
 * Metrics Service
 * Handles metrics and health check API calls
 */

import apiClient from './api'
import type { ConnectionMetricsResponse, HealthResponse, ReadyResponse } from '../types/api'

/**
 * Get metrics for a specific connection
 */
export async function getConnectionMetrics(
  connectionId: string
): Promise<ConnectionMetricsResponse> {
  const response = await apiClient.get<ConnectionMetricsResponse>(
    `/api/v1/connections/${connectionId}/metrics`
  )
  return response.data
}

/**
 * Get API health status
 */
export async function getHealthStatus(): Promise<HealthResponse> {
  const response = await apiClient.get<HealthResponse>('/health')
  return response.data
}

/**
 * Get API readiness status
 */
export async function getReadyStatus(): Promise<ReadyResponse> {
  const response = await apiClient.get<ReadyResponse>('/ready')
  return response.data
}

export const metricsService = {
  getMetrics: getConnectionMetrics,
  getHealth: getHealthStatus,
  getReady: getReadyStatus,
}
