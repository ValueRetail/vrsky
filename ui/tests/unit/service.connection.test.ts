/**
 * Connection Service Tests
 */

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { connectionService } from '@/services/connectionService'
import type { GetConnectionResponse, ListConnectionsResponse } from '@/types/api'

// Mock the API client
vi.mock('@/services/api', () => ({
  default: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}))

import apiClient from '@/services/api'

const mockConnectionResponse: GetConnectionResponse = {
  id: 'conn-1',
  tenant_id: 'tenant-1',
  name: 'Test Connection',
  description: 'Test',
  status: 'stopped',
  source_config: { type: 'http', url: 'http://example.com', method: 'GET' },
  converter_config: { type: 'schema', input_schema: {} },
  filter_config: { type: 'rules', rules: [] },
  destination_config: { type: 'http', url: 'http://example.com', method: 'POST' },
  metrics: {},
  created_at: '2024-01-01T00:00:00Z',
  updated_at: '2024-01-01T00:00:00Z',
}

describe('Connection Service', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  describe('Get Connection', () => {
    it('should fetch a single connection', async () => {
      vi.mocked(apiClient.get).mockResolvedValue({
        data: mockConnectionResponse,
      })

      const result = await connectionService.get('conn-1')
      expect(result).toEqual(mockConnectionResponse)
      expect(apiClient.get).toHaveBeenCalledWith('/api/v1/connections/conn-1')
    })

    it('should handle connection fetch errors', async () => {
      const error = new Error('Network error')
      vi.mocked(apiClient.get).mockRejectedValue(error)

      await expect(connectionService.get('conn-1')).rejects.toThrow('Network error')
    })
  })

  describe('List Connections', () => {
    it('should fetch list of connections', async () => {
      const response: ListConnectionsResponse = {
        connections: [mockConnectionResponse],
        total: 1,
        page: 1,
        page_size: 10,
      }

      vi.mocked(apiClient.get).mockResolvedValue({ data: response })

      const result = await connectionService.list()
      expect(result).toEqual(response)
      expect(apiClient.get).toHaveBeenCalledWith('/api/v1/connections?limit=20&offset=0')
    })

    it('should support pagination', async () => {
      const response: ListConnectionsResponse = {
        connections: [mockConnectionResponse],
        total: 1,
        page: 2,
        page_size: 10,
      }

      vi.mocked(apiClient.get).mockResolvedValue({ data: response })

      await connectionService.list(2, 10)
      expect(apiClient.get).toHaveBeenCalledWith('/api/v1/connections?limit=10&offset=10')
    })
  })

  describe('Create Connection', () => {
    it('should create a new connection', async () => {
      vi.mocked(apiClient.post).mockResolvedValue({
        data: mockConnectionResponse,
      })

      const createData = {
        name: 'Test Connection',
        description: 'Test',
        source_config: { type: 'http', url: 'http://example.com', method: 'GET' },
        converter_config: { type: 'schema', input_schema: {} },
        filter_config: { type: 'rules', rules: [] },
        destination_config: { type: 'http', url: 'http://example.com', method: 'POST' },
      }

      const result = await connectionService.create(createData as any)
      expect(result).toEqual(mockConnectionResponse)
      expect(apiClient.post).toHaveBeenCalledWith('/api/v1/connections', createData)
    })
  })

  describe('Update Connection', () => {
    it('should update a connection', async () => {
      const updated = { ...mockConnectionResponse, name: 'Updated' }
      vi.mocked(apiClient.put).mockResolvedValue({ data: updated })

      const result = await connectionService.update('conn-1', { name: 'Updated' } as any)
      expect(result).toEqual(updated)
      expect(apiClient.put).toHaveBeenCalledWith('/api/v1/connections/conn-1', {
        name: 'Updated',
      })
    })
  })

  describe('Delete Connection', () => {
    it('should delete a connection', async () => {
      vi.mocked(apiClient.delete).mockResolvedValue({
        data: { success: true, message: 'Deleted' },
      })

      await connectionService.delete('conn-1')
      expect(apiClient.delete).toHaveBeenCalledWith('/api/v1/connections/conn-1')
    })
  })

   describe('Start Connection', () => {
    it('should start a connection', async () => {
      const started = { ...mockConnectionResponse, status: 'running' as const }
      
      // First call is POST to /start, second is GET to fetch the connection
      vi.mocked(apiClient.post).mockResolvedValue({ data: { success: true } })
      vi.mocked(apiClient.get).mockResolvedValue({ data: started })

      const result = await connectionService.start('conn-1')
      expect(result).toEqual(started)
      expect(apiClient.post).toHaveBeenCalledWith('/api/v1/connections/conn-1/start', {})
      expect(apiClient.get).toHaveBeenCalledWith('/api/v1/connections/conn-1')
    })
  })

  describe('Stop Connection', () => {
    it('should stop a connection', async () => {
      const stopped = { ...mockConnectionResponse, status: 'stopped' as const }
      
      // First call is POST to /stop, second is GET to fetch the connection
      vi.mocked(apiClient.post).mockResolvedValue({ data: { success: true } })
      vi.mocked(apiClient.get).mockResolvedValue({ data: stopped })

      const result = await connectionService.stop('conn-1')
      expect(result).toEqual(stopped)
      expect(apiClient.post).toHaveBeenCalledWith('/api/v1/connections/conn-1/stop', {})
      expect(apiClient.get).toHaveBeenCalledWith('/api/v1/connections/conn-1')
    })
  })
})
