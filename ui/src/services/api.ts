/**
 * API Client
 * Axios instance with interceptors for X-Tenant-ID header and error handling
 */

import axios, { AxiosError } from 'axios'
import type { AxiosInstance } from 'axios'
import { config } from '@/config/env'
import { getSessionToken } from '@/services/authService'
import type { APIError } from '@/types/api'

// Dynamic tenant ID — updated by authStore when switching tenants
let activeTenantId: string = config.tenantId

export function setActiveTenantId(id: string) {
  activeTenantId = id
}

export function getActiveTenantId(): string {
  return activeTenantId
}

// Create Axios instance.
//
// In dev (Vite on :5173, API on :3000) we deliberately leave baseURL empty
// so axios issues relative requests like /api/v1/connections. Vite's dev
// server proxies /api/* → :3000, which means the browser sees a SAME-ORIGIN
// request. That side-steps the SameSite=Lax problem where cross-origin XHR
// would not carry the vrsky_session cookie (#68).
//
// In production the UI and API are served from the same origin behind
// Traefik, so the same relative path works without a proxy.
//
// withCredentials=true is still needed because axios's default for HTTP
// auth-bearing same-origin requests is fine, but we want to be explicit
// for the rare cases (e.g. the JSONL export blob) that hit the API in
// modes axios's default would otherwise strip cookies on.
const apiClient: AxiosInstance = axios.create({
  baseURL: '',
  timeout: 30000,
  withCredentials: true,
  headers: {
    'Content-Type': 'application/json',
  },
})

// Request interceptor: Add X-Tenant-ID header
apiClient.interceptors.request.use(
  (requestConfig) => {
    requestConfig.headers['X-Tenant-ID'] = activeTenantId
    const token = getSessionToken()
    if (token) {
      requestConfig.headers['Authorization'] = `Bearer ${token}`
    }
    return requestConfig
  },
  (error) => Promise.reject(error)
)

// VRSkyAPIError is what apiClient rejects with on a non-2xx response.
// It IS an Error so `instanceof Error` catches behave correctly AND it
// carries the structured server payload (code + details) for callers
// that want to branch on it.
export class VRSkyAPIError extends Error implements APIError {
  code: string
  details?: Record<string, unknown>
  constructor(message: string, code: string, details?: Record<string, unknown>) {
    super(message)
    this.name = 'VRSkyAPIError'
    this.code = code
    this.details = details
  }
}

// Response interceptor: Handle errors
apiClient.interceptors.response.use(
  (response) => response,
  (error: AxiosError) => {
    if (!error.response) {
      return Promise.reject(
        new VRSkyAPIError(
          'Network request failed. Please check your connection.',
          'NETWORK_ERROR',
          { originalError: error.message },
        ),
      )
    }

    const status = error.response.status
    const data = (error.response.data ?? {}) as Record<string, unknown>
    const message = data.message ? String(data.message) : `HTTP Error ${status}`
    const code = data.error ? String(data.error) : `HTTP_${status}`
    return Promise.reject(new VRSkyAPIError(message, code, { status, ...data }))
  }
)

export default apiClient
