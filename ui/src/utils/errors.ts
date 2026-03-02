/**
 * Error Utilities
 * Helper functions for error handling and formatting
 */

import type { APIError } from '../types/api'

/**
 * Check if an error is an API error
 */
export function isAPIError(error: unknown): error is APIError {
  return (
    typeof error === 'object' &&
    error !== null &&
    'code' in error &&
    'message' in error
  )
}

/**
 * Get error message from various error types
 */
export function getErrorMessage(error: unknown): string {
  if (isAPIError(error)) {
    return error.message
  }

  if (error instanceof Error) {
    return error.message
  }

  if (typeof error === 'string') {
    return error
  }

  return 'An unknown error occurred'
}

/**
 * Format error for display
 */
export function formatError(error: unknown): { title: string; message: string } {
  if (isAPIError(error)) {
    const title = error.code.replace(/_/g, ' ')
    return {
      title,
      message: error.message,
    }
  }

  if (error instanceof Error) {
    return {
      title: error.name || 'Error',
      message: error.message,
    }
  }

  return {
    title: 'Unknown Error',
    message: 'An unexpected error occurred',
  }
}

/**
 * Check if error is a network error
 */
export function isNetworkError(error: unknown): boolean {
  return (
    isAPIError(error) &&
    (error.code === 'NETWORK_ERROR' || error.code.startsWith('HTTP_'))
  )
}

/**
 * Check if error is a validation error
 */
export function isValidationError(error: unknown): boolean {
  return isAPIError(error) && error.code.includes('VALIDATION')
}

/**
 * Check if error is a not found error
 */
export function isNotFoundError(error: unknown): boolean {
  return isAPIError(error) && error.code === 'HTTP_404'
}

/**
 * Check if error is an unauthorized error
 */
export function isUnauthorizedError(error: unknown): boolean {
  return isAPIError(error) && (error.code === 'HTTP_401' || error.code === 'HTTP_403')
}

export const errorUtils = {
  isAPIError,
  getErrorMessage,
  formatError,
  isNetworkError,
  isValidationError,
  isNotFoundError,
  isUnauthorizedError,
}
