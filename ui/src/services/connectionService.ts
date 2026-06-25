/**
 * Connection Service
 * Handles all connection-related API calls
 */

import apiClient from './api'
import type { Connection } from '../types/models'
import type {
  GetConnectionResponse,
  ListConnectionsResponse,
  CreateConnectionResponse,
  UpdateConnectionResponse,
  DeleteConnectionResponse,
  StartConnectionResponse,
  StopConnectionResponse,
} from '../types/api'

/**
 * Create a new connection
 */
export async function createConnection(data: unknown): Promise<Connection> {
  const response = await apiClient.post<CreateConnectionResponse>(
    '/api/v1/connections',
    data
  )
  return response.data as unknown as Connection
}

/**
 * Get a single connection by ID
 */
export async function getConnection(id: string): Promise<Connection> {
  const response = await apiClient.get<GetConnectionResponse | { data: GetConnectionResponse }>(
    `/api/v1/connections/${id}`
  )
  // GET /connections/:id wraps the connection in a { data: ... } envelope
  // (unlike the list endpoint). Unwrap it; fall back to the body itself for
  // any endpoint/version that returns the connection unwrapped. Without this,
  // the detail page rendered an envelope object whose fields were all
  // undefined → blank ID/Status and "Invalid Date".
  const body = response.data as Record<string, unknown>
  return ((body.data ?? body) as unknown) as Connection
}

/**
 * List all connections
 */
export async function listConnections(
  page?: number,
  pageSize?: number
): Promise<ListConnectionsResponse> {
  const params = new URLSearchParams()
  const limit = pageSize || 20
  const offset = page !== undefined ? (page - 1) * limit : 0
  params.append('limit', limit.toString())
  params.append('offset', offset.toString())

  const queryString = params.toString()
  const url = queryString ? `/api/v1/connections?${queryString}` : '/api/v1/connections'

  const response = await apiClient.get<ListConnectionsResponse>(url)
  return response.data
}

/**
 * Update an existing connection
 */
export async function updateConnection(
  id: string,
  data: unknown
): Promise<Connection> {
  const response = await apiClient.put<UpdateConnectionResponse>(
    `/api/v1/connections/${id}`,
    data
  )
  return response.data as unknown as Connection
}

/**
 * Delete a connection
 */
export async function deleteConnection(id: string): Promise<void> {
  await apiClient.delete<DeleteConnectionResponse>(`/api/v1/connections/${id}`)
}

/**
 * Start a connection
 */
export async function startConnection(id: string): Promise<Connection> {
  await apiClient.post<StartConnectionResponse>(
    `/api/v1/connections/${id}/start`,
    {}
  )
  // Return full connection object by fetching it
  return getConnection(id)
}

/**
 * Stop a connection
 */
export async function stopConnection(id: string): Promise<Connection> {
  await apiClient.post<StopConnectionResponse>(
    `/api/v1/connections/${id}/stop`,
    {}
  )
  // Return full connection object by fetching it
  return getConnection(id)
}

export const connectionService = {
  create: createConnection,
  get: getConnection,
  list: listConnections,
  update: updateConnection,
  delete: deleteConnection,
  start: startConnection,
  stop: stopConnection,
}
