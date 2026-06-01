/**
 * OAuth 2.0 framework service (Phase 2A — #75)
 *
 * Talks to the management-api /api/v1/oauth/* endpoints. Uses the shared
 * apiClient so the X-Tenant-ID header + session cookie are attached by its
 * interceptors and requests stay same-origin via the Vite proxy (the
 * load-bearing cookie rule — do not swap this for an absolute baseURL).
 */

import apiClient from './api'

export interface OAuthProvider {
  id: string
  name: string
  provider_type: string
  client_id: string
  auth_url: string
  token_url: string
  revoke_url?: string
  scopes: string[]
  redirect_url: string
  extra_params?: Record<string, string>
}

export interface OAuthGrant {
  id: string
  provider_id: string
  provider_type: string
  provider_name: string
  connection_id?: string
  user_identifier?: string
  scopes_granted: string[]
  expires_at?: string
  last_refreshed_at?: string
  revoked_at?: string
  refresh_failed_at?: string
  refresh_failure_reason?: string
  needs_reconnect: boolean
}

export interface CreateProviderInput {
  name: string
  provider_type: string
  client_id: string
  client_secret: string
  redirect_url: string
  scopes?: string[]
  auth_url?: string
  token_url?: string
  revoke_url?: string
  extra_params?: Record<string, string>
}

// --- Providers ---

export async function listProviders(): Promise<OAuthProvider[]> {
  const res = await apiClient.get<{ providers: OAuthProvider[] }>('/api/v1/oauth/providers')
  return res.data.providers ?? []
}

export async function createProvider(input: CreateProviderInput): Promise<OAuthProvider> {
  const res = await apiClient.post<OAuthProvider>('/api/v1/oauth/providers', input)
  return res.data
}

export async function deleteProvider(id: string): Promise<void> {
  await apiClient.delete(`/api/v1/oauth/providers/${id}`)
}

// --- Grants ---

export async function listGrants(): Promise<OAuthGrant[]> {
  const res = await apiClient.get<{ grants: OAuthGrant[] }>('/api/v1/oauth/grants')
  return res.data.grants ?? []
}

export async function getGrant(id: string): Promise<OAuthGrant> {
  const res = await apiClient.get<OAuthGrant>(`/api/v1/oauth/grants/${id}`)
  return res.data
}

export async function revokeGrant(id: string): Promise<{ revoked: boolean; provider_warning?: string }> {
  const res = await apiClient.post<{ revoked: boolean; provider_warning?: string }>(
    `/api/v1/oauth/grants/${id}/revoke`,
    {}
  )
  return res.data
}

// --- Authorization flow ---

export interface StartAuthInput {
  connection_id?: string
  // Provider-specific extras carried to the callback (e.g. Shopify's shop).
  extra_params?: Record<string, string>
}

/**
 * Begins the auth-code flow. Returns the provider authorize URL the caller
 * should open in a popup; the server has set the short-lived state/verifier
 * cookies needed by the callback.
 */
export async function startAuth(providerId: string, input: StartAuthInput = {}): Promise<string> {
  const res = await apiClient.post<{ authorize_url: string }>(
    `/api/v1/oauth/providers/${providerId}/start`,
    input
  )
  return res.data.authorize_url
}
