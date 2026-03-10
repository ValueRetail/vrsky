/**
 * Test Data Service
 * Handles test message and auto-generator API calls
 */

import apiClient from './api'
import type {
  TestMessageResponse,
  AutoGeneratorStatusResponse,
  StartGeneratorResponse,
  StopGeneratorResponse,
} from '../types/api'

/**
 * Send a test message to a connection
 */
export async function sendTestMessage(connectionId: string, message: string): Promise<void> {
  let payload: unknown
  try {
    payload = JSON.parse(message)
  } catch {
    payload = message
  }

  await apiClient.post<TestMessageResponse>(`/api/v1/connections/${connectionId}/test-message`, {
    payload,
  })
}

/**
 * Start the auto-generator for a connection
 */
export async function startAutoGenerator(
  connectionId: string,
  rate: number = 1
): Promise<void> {
  await apiClient.post<StartGeneratorResponse>(`/api/v1/connections/${connectionId}/auto-generator/start`, {
    rate_per_second: rate,
  })
}

/**
 * Stop the auto-generator for a connection
 */
export async function stopAutoGenerator(connectionId: string): Promise<void> {
  await apiClient.post<StopGeneratorResponse>(`/api/v1/connections/${connectionId}/auto-generator/stop`, {})
}

/**
 * Get auto-generator status for a connection
 */
export async function getAutoGeneratorStatus(
  connectionId: string
): Promise<AutoGeneratorStatusResponse> {
  const response = await apiClient.get<AutoGeneratorStatusResponse>(
    `/api/v1/connections/${connectionId}/auto-generator/status`
  )
  return response.data
}

export const testDataService = {
  sendTestMessage,
  startAutoGenerator,
  stopAutoGenerator,
  getGeneratorStatus: getAutoGeneratorStatus,
}
