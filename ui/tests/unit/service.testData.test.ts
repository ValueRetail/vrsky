/**
 * Test Data Service Tests
 */

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { testDataService } from '@/services/testDataService'

vi.mock('@/services/api', () => ({
  default: {
    post: vi.fn(),
    get: vi.fn(),
  },
}))

import apiClient from '@/services/api'

describe('Test Data Service', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  describe('Send Test Message', () => {
    it('should send a test message', async () => {
      vi.mocked(apiClient.post).mockResolvedValue({
        data: {
          success: true,
          message_id: 'msg-1',
          message: 'Message sent',
        },
      })

      await testDataService.sendTestMessage('conn-1', '{"test": "data"}')

      expect(apiClient.post).toHaveBeenCalledWith('/test-message', {
        connection_id: 'conn-1',
        message: '{"test": "data"}',
      })
    })

    it('should handle test message errors', async () => {
      const error = new Error('Failed to send')
      vi.mocked(apiClient.post).mockRejectedValue(error)

      await expect(
        testDataService.sendTestMessage('conn-1', '{"test": "data"}')
      ).rejects.toThrow('Failed to send')
    })
  })

  describe('Auto Generator', () => {
    it('should start auto generator with defaults', async () => {
      vi.mocked(apiClient.post).mockResolvedValue({
        data: {
          connection_id: 'conn-1',
          running: true,
          rate: 1,
          message_size: 'small',
          started_at: new Date().toISOString(),
        },
      })

      await testDataService.startAutoGenerator('conn-1')

      expect(apiClient.post).toHaveBeenCalledWith('/auto-generator/start', {
        connection_id: 'conn-1',
        rate: 1,
        message_size: 'small',
      })
    })

    it('should start auto generator with custom rate', async () => {
      vi.mocked(apiClient.post).mockResolvedValue({
        data: {
          connection_id: 'conn-1',
          running: true,
          rate: 100,
          message_size: 'large',
          started_at: new Date().toISOString(),
        },
      })

      await testDataService.startAutoGenerator('conn-1', 100, 'large')

      expect(apiClient.post).toHaveBeenCalledWith('/auto-generator/start', {
        connection_id: 'conn-1',
        rate: 100,
        message_size: 'large',
      })
    })

    it('should stop auto generator', async () => {
      vi.mocked(apiClient.post).mockResolvedValue({
        data: {
          connection_id: 'conn-1',
          running: false,
          stopped_at: new Date().toISOString(),
          total_generated: 500,
        },
      })

      await testDataService.stopAutoGenerator('conn-1')

      expect(apiClient.post).toHaveBeenCalledWith('/auto-generator/stop', {
        connection_id: 'conn-1',
      })
    })

    it('should get auto generator status', async () => {
      const status = {
        connection_id: 'conn-1',
        running: true,
        rate: 50,
        message_size: 'medium',
        total_generated: 2500,
        started_at: new Date().toISOString(),
      }

      vi.mocked(apiClient.get).mockResolvedValue({ data: status })

      const result = await testDataService.getGeneratorStatus('conn-1')

      expect(result).toEqual(status)
      expect(apiClient.get).toHaveBeenCalledWith(
        '/auto-generator/status?connection_id=conn-1'
      )
    })
  })

  describe('Error Handling', () => {
    it('should handle network errors', async () => {
      const error = new Error('Network timeout')
      vi.mocked(apiClient.post).mockRejectedValue(error)

      await expect(
        testDataService.startAutoGenerator('conn-1', 10)
      ).rejects.toThrow('Network timeout')
    })

    it('should handle API errors', async () => {
      const error = new Error('Invalid connection')
      vi.mocked(apiClient.post).mockRejectedValue(error)

      await expect(
        testDataService.sendTestMessage('invalid', '{"test": "data"}')
      ).rejects.toThrow('Invalid connection')
    })
  })
})
