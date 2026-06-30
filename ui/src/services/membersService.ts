/**
 * Tenant members service — Phase 1D (#69).
 *
 * The /members endpoints sit under /api/v1/tenants/{id}, not the
 * X-Tenant-ID-header world, so we pass the tenant ID explicitly in the
 * URL rather than relying on the apiClient interceptor.
 */

import apiClient from './api'

export type TenantRole = 'owner' | 'admin' | 'editor' | 'viewer'

export interface TenantMember {
  user_id: string
  tenant_id: string
  email: string
  full_name?: string
  role: TenantRole
  invited_at: string
  joined_at?: string
}

interface Envelope<T> { data: T }

export async function listMembers(tenantID: string): Promise<TenantMember[]> {
  const resp = await apiClient.get<Envelope<TenantMember[]>>(
    `/api/v1/tenants/${tenantID}/members`,
  )
  return resp.data.data ?? []
}

export async function addMember(
  tenantID: string,
  email: string,
  role: TenantRole,
): Promise<TenantMember> {
  const resp = await apiClient.post<Envelope<TenantMember>>(
    `/api/v1/tenants/${tenantID}/members`,
    { email, role },
  )
  return resp.data.data
}

export async function setMemberRole(
  tenantID: string,
  userID: string,
  role: TenantRole,
): Promise<void> {
  await apiClient.put(`/api/v1/tenants/${tenantID}/members/${userID}`, { role })
}

export async function removeMember(tenantID: string, userID: string): Promise<void> {
  await apiClient.delete(`/api/v1/tenants/${tenantID}/members/${userID}`)
}

// ===== Pending invites (#130) =====

export interface TenantInvite {
  id: string
  tenant_id: string
  email: string
  role: TenantRole
  token?: string
  status: 'pending' | 'accepted' | 'revoked'
  created_at: string
  expires_at: string
  accepted_at?: string
}

// inviteMember creates an invite for an email. If that email already belongs to
// a registered user the server adds them directly (response has `added_member`);
// otherwise a pending invite is returned (with its accept token).
export async function inviteMember(
  tenantID: string,
  email: string,
  role: TenantRole,
): Promise<{ invite?: TenantInvite; added?: TenantMember }> {
  const resp = await apiClient.post<Envelope<TenantInvite & { added_member?: TenantMember }>>(
    `/api/v1/tenants/${tenantID}/invites`,
    { email, role },
  )
  const d = resp.data.data
  if (d && (d as { added_member?: TenantMember }).added_member) {
    return { added: (d as { added_member?: TenantMember }).added_member }
  }
  return { invite: d as TenantInvite }
}

export async function listInvites(tenantID: string): Promise<TenantInvite[]> {
  const resp = await apiClient.get<Envelope<TenantInvite[]>>(
    `/api/v1/tenants/${tenantID}/invites`,
  )
  return resp.data.data ?? []
}

export async function resendInvite(tenantID: string, inviteID: string): Promise<TenantInvite> {
  const resp = await apiClient.post<Envelope<TenantInvite>>(
    `/api/v1/tenants/${tenantID}/invites/${inviteID}/resend`,
    {},
  )
  return resp.data.data
}

export async function revokeInvite(tenantID: string, inviteID: string): Promise<void> {
  await apiClient.delete(`/api/v1/tenants/${tenantID}/invites/${inviteID}`)
}

// acceptInvite is called by the logged-in invitee from the accept page.
export async function acceptInvite(token: string): Promise<TenantMember> {
  const resp = await apiClient.post<Envelope<TenantMember>>(`/api/v1/invites/accept`, { token })
  return resp.data.data
}

// inviteLink builds the shareable accept URL for an invite token.
export function inviteLink(token: string): string {
  return `${window.location.origin}/invite?token=${encodeURIComponent(token)}`
}
