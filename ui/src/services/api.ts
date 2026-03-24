/**
 * API Client
 * Axios instance with interceptors for X-Tenant-ID header and error handling
 */

import axios, { AxiosError } from 'axios'
import type { AxiosInstance } from 'axios'
import { config } from '@/config/env'
import type { APIError } from '@/types/api'

// Dynamic tenant ID — updated by authStore when switching tenants
let activeTenantId: string = config.tenantId

export function setActiveTenantId(id: string) {
  activeTenantId = id
}

export function getActiveTenantId(): string {
  return activeTenantId
}

// Create Axios instance
const apiClient: AxiosInstance = axios.create({
  baseURL: config.apiUrl,
  timeout: 30000,
  headers: {
    'Content-Type': 'application/json',
  },
})

// Request interceptor: Add X-Tenant-ID header
apiClient.interceptors.request.use(
  (requestConfig) => {
    requestConfig.headers['X-Tenant-ID'] = activeTenantId
    return requestConfig
  },
  (error) => Promise.reject(error)
)

// Response interceptor: Handle errors
apiClient.interceptors.response.use(
  (response) => response,
  (error: AxiosError) => {
    if (!error.response) {
      // Network error
      const apiError: APIError = {
        code: 'NETWORK_ERROR',
        message: 'Network request failed. Please check your connection.',
        details: { originalError: error.message },
      }
      return Promise.reject(apiError)
    }

    // Server error
    const status = error.response.status
    const data = error.response.data as Record<string, unknown>

    const apiError: APIError = {
      code: `HTTP_${status}`,
      message: data.message ? String(data.message) : `HTTP Error ${status}`,
      details: { status, ...data },
    }

    return Promise.reject(apiError)
  }
)

export default apiClient
