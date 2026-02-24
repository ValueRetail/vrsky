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
  await apiClient.post<TestMessageResponse>('/test-message', {
    connection_id: connectionId,
    message,
  })
}

/**
 * Start the auto-generator for a connection
 */
export async function startAutoGenerator(
  connectionId: string,
  rate: number = 1,
  messageSize: 'small' | 'medium' | 'large' = 'small'
): Promise<void> {
  await apiClient.post<StartGeneratorResponse>('/auto-generator/start', {
    connection_id: connectionId,
    rate,
    message_size: messageSize,
  })
}

/**
 * Stop the auto-generator for a connection
 */
export async function stopAutoGenerator(connectionId: string): Promise<void> {
  await apiClient.post<StopGeneratorResponse>('/auto-generator/stop', {
    connection_id: connectionId,
  })
}

/**
 * Get auto-generator status for a connection
 */
export async function getAutoGeneratorStatus(
  connectionId: string
): Promise<AutoGeneratorStatusResponse> {
  const response = await apiClient.get<AutoGeneratorStatusResponse>(
    `/auto-generator/status?connection_id=${connectionId}`
  )
  return response.data
}

export const testDataService = {
  sendTestMessage,
  startAutoGenerator,
  stopAutoGenerator,
  getGeneratorStatus: getAutoGeneratorStatus,
}
