/**
 * Auth Service
 * Handles authentication API calls
 * Note: Auth routes do NOT require X-Tenant-ID header
 */

import axios from 'axios'
import type { AxiosInstance } from 'axios'
import { config } from '@/config/env'
import type {
  RegisterRequest,
  LoginRequest,
  ChangePasswordRequest,
  ForgotPasswordRequest,
  ResetPasswordRequest,
  AuthResponse,
  MeResponse,
} from '@/types/models'

// Session token storage key
const SESSION_TOKEN_KEY = 'vrsky_session_token'

// Create a separate Axios instance for auth (no X-Tenant-ID)
const authClient: AxiosInstance = axios.create({
  baseURL: config.apiUrl,
  timeout: 30000,
  headers: {
    'Content-Type': 'application/json',
  },
})

/**
 * Get the stored session token from sessionStorage
 */
export function getSessionToken(): string | null {
  return sessionStorage.getItem(SESSION_TOKEN_KEY)
}

/**
 * Store the session token in sessionStorage
 */
export function setSessionToken(token: string): void {
  sessionStorage.setItem(SESSION_TOKEN_KEY, token)
}

/**
 * Clear the session token from sessionStorage
 */
export function clearSessionToken(): void {
  sessionStorage.removeItem(SESSION_TOKEN_KEY)
}

/**
 * Check if user has a session token (may be expired)
 */
export function hasSessionToken(): boolean {
  return !!getSessionToken()
}

/**
 * Register a new user
 */
export async function register(data: RegisterRequest): Promise<AuthResponse> {
  const response = await authClient.post<AuthResponse>('/api/v1/auth/register', data)
  return response.data
}

/**
 * Login with email and password
 */
export async function login(data: LoginRequest): Promise<AuthResponse> {
  const response = await authClient.post<AuthResponse>('/api/v1/auth/login', data)
  
  // Store the session token if login was successful
  if (response.data.success && response.data.session_token) {
    setSessionToken(response.data.session_token)
  }
  
  return response.data
}

/**
 * Get the current authenticated user and their tenants
 */
export async function getMe(): Promise<MeResponse | null> {
  const token = getSessionToken()
  if (!token) {
    return null
  }

  try {
    const response = await authClient.get<MeResponse>('/api/v1/auth/me', {
      headers: {
        Authorization: `Bearer ${token}`,
      },
    })
    return response.data
  } catch (error) {
    // If unauthorized, clear the invalid token
    if (axios.isAxiosError(error) && error.response?.status === 401) {
      clearSessionToken()
    }
    return null
  }
}

/**
 * Logout the current user
 */
export async function logout(): Promise<void> {
  const token = getSessionToken()
  if (!token) {
    return
  }

  try {
    await authClient.post('/api/v1/auth/logout', {}, {
      headers: {
        Authorization: `Bearer ${token}`,
      },
    })
  } finally {
    // Always clear the local token, even if server request fails
    clearSessionToken()
  }
}

/**
 * Verify email with token
 */
export async function verifyEmail(token: string): Promise<AuthResponse> {
  const response = await authClient.get<AuthResponse>(`/api/v1/auth/verify-email?token=${encodeURIComponent(token)}`)
  return response.data
}

/**
 * Request password reset email
 */
export async function forgotPassword(data: ForgotPasswordRequest): Promise<AuthResponse> {
  const response = await authClient.post<AuthResponse>('/api/v1/auth/forgot-password', data)
  return response.data
}

/**
 * Reset password with token
 */
export async function resetPassword(data: ResetPasswordRequest): Promise<AuthResponse> {
  const response = await authClient.post<AuthResponse>('/api/v1/auth/reset-password', data)
  return response.data
}

/**
 * Change password for authenticated user
 */
export async function changePassword(data: ChangePasswordRequest): Promise<AuthResponse> {
  const token = getSessionToken()
  if (!token) {
    throw new Error('Not authenticated')
  }

  const response = await authClient.post<AuthResponse>('/api/v1/auth/change-password', data, {
    headers: {
      Authorization: `Bearer ${token}`,
    },
  })
  return response.data
}

/**
 * Delete the current user's account
 */
export async function deleteAccount(): Promise<void> {
  const token = getSessionToken()
  if (!token) {
    throw new Error('Not authenticated')
  }

  await authClient.delete('/api/v1/auth/me', {
    headers: {
      Authorization: `Bearer ${token}`,
    },
  })
  clearSessionToken()
}

export default {
  register,
  login,
  logout,
  getMe,
  verifyEmail,
  forgotPassword,
  resetPassword,
  changePassword,
  deleteAccount,
  getSessionToken,
  setSessionToken,
  clearSessionToken,
  hasSessionToken,
}
