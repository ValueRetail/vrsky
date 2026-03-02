/**
 * Error Utilities Tests
 */

import { describe, it, expect } from 'vitest'
import {
  isAPIError,
  getErrorMessage,
  formatError,
  isNetworkError,
  isValidationError,
  isNotFoundError,
  isUnauthorizedError,
} from '@/utils/errors'
import type { APIError } from '@/types/api'

describe('Error Utilities', () => {
  describe('isAPIError', () => {
    it('should return true for valid API errors', () => {
      const error: APIError = {
        code: 'TEST_ERROR',
        message: 'Test error message',
      }
      expect(isAPIError(error)).toBe(true)
    })

    it('should return true for API errors with details', () => {
      const error: APIError = {
        code: 'TEST_ERROR',
        message: 'Test error message',
        details: { field: 'value' },
      }
      expect(isAPIError(error)).toBe(true)
    })

    it('should return false for non-API errors', () => {
      expect(isAPIError(new Error('test'))).toBe(false)
      expect(isAPIError('string error')).toBe(false)
      expect(isAPIError(null)).toBe(false)
      expect(isAPIError(undefined)).toBe(false)
      expect(isAPIError({})).toBe(false)
    })

    it('should return false for objects missing required fields', () => {
      expect(isAPIError({ code: 'ERROR' })).toBe(false)
      expect(isAPIError({ message: 'error' })).toBe(false)
    })
  })

  describe('getErrorMessage', () => {
    it('should return message from API error', () => {
      const error: APIError = {
        code: 'TEST_ERROR',
        message: 'API error message',
      }
      expect(getErrorMessage(error)).toBe('API error message')
    })

    it('should return message from Error object', () => {
      const error = new Error('Standard error message')
      expect(getErrorMessage(error)).toBe('Standard error message')
    })

    it('should return the string as-is', () => {
      expect(getErrorMessage('String error')).toBe('String error')
    })

    it('should return default message for unknown errors', () => {
      expect(getErrorMessage(null)).toBe('An unknown error occurred')
      expect(getErrorMessage(undefined)).toBe('An unknown error occurred')
      expect(getErrorMessage({})).toBe('An unknown error occurred')
      expect(getErrorMessage(123)).toBe('An unknown error occurred')
    })
  })

  describe('formatError', () => {
    it('should format API error correctly', () => {
      const error: APIError = {
        code: 'NOT_FOUND_ERROR',
        message: 'Resource not found',
      }
      const result = formatError(error)
      expect(result.title).toBe('NOT FOUND ERROR')
      expect(result.message).toBe('Resource not found')
    })

    it('should format Error object correctly', () => {
      const error = new TypeError('Invalid type')
      const result = formatError(error)
      expect(result.title).toBe('TypeError')
      expect(result.message).toBe('Invalid type')
    })

    it('should format unknown errors with defaults', () => {
      const result = formatError(null)
      expect(result.title).toBe('Unknown Error')
      expect(result.message).toBe('An unexpected error occurred')
    })
  })

  describe('isNetworkError', () => {
    it('should return true for network errors', () => {
      const error: APIError = {
        code: 'NETWORK_ERROR',
        message: 'Network failed',
      }
      expect(isNetworkError(error)).toBe(true)
    })

    it('should return true for HTTP errors', () => {
      const error: APIError = {
        code: 'HTTP_500',
        message: 'Server error',
      }
      expect(isNetworkError(error)).toBe(true)
    })

    it('should return false for non-network errors', () => {
      const error: APIError = {
        code: 'VALIDATION_ERROR',
        message: 'Validation failed',
      }
      expect(isNetworkError(error)).toBe(false)
    })

    it('should return false for non-API errors', () => {
      expect(isNetworkError(new Error('test'))).toBe(false)
      expect(isNetworkError('string')).toBe(false)
    })
  })

  describe('isValidationError', () => {
    it('should return true for validation errors', () => {
      const error: APIError = {
        code: 'VALIDATION_ERROR',
        message: 'Invalid input',
      }
      expect(isValidationError(error)).toBe(true)
    })

    it('should return true for field validation errors', () => {
      const error: APIError = {
        code: 'FIELD_VALIDATION_ERROR',
        message: 'Field invalid',
      }
      expect(isValidationError(error)).toBe(true)
    })

    it('should return false for non-validation errors', () => {
      const error: APIError = {
        code: 'NETWORK_ERROR',
        message: 'Network failed',
      }
      expect(isValidationError(error)).toBe(false)
    })
  })

  describe('isNotFoundError', () => {
    it('should return true for 404 errors', () => {
      const error: APIError = {
        code: 'HTTP_404',
        message: 'Not found',
      }
      expect(isNotFoundError(error)).toBe(true)
    })

    it('should return false for other HTTP errors', () => {
      const error: APIError = {
        code: 'HTTP_500',
        message: 'Server error',
      }
      expect(isNotFoundError(error)).toBe(false)
    })
  })

  describe('isUnauthorizedError', () => {
    it('should return true for 401 errors', () => {
      const error: APIError = {
        code: 'HTTP_401',
        message: 'Unauthorized',
      }
      expect(isUnauthorizedError(error)).toBe(true)
    })

    it('should return true for 403 errors', () => {
      const error: APIError = {
        code: 'HTTP_403',
        message: 'Forbidden',
      }
      expect(isUnauthorizedError(error)).toBe(true)
    })

    it('should return false for other errors', () => {
      const error: APIError = {
        code: 'HTTP_404',
        message: 'Not found',
      }
      expect(isUnauthorizedError(error)).toBe(false)
    })
  })
})
