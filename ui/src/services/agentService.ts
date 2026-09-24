/**
 * Remote agents (#266).
 *
 * An agent is a small program on a customer machine that connects out to VRSky
 * and makes that machine usable as a pipeline input or output. This service is
 * the management side: minting one-time registration tokens, and listing /
 * renaming / revoking agents. The workspace comes from X-Tenant-ID, added by
 * apiClient.
 */

import apiClient from './api'

/** One directory an agent reported. Names and modes only — the path lives in
 *  the agent's own config file and never leaves the machine. */
export interface AgentDirectory {
  name: string
  mode: 'read' | 'write'
}

export interface Agent {
  id: string
  tenant_id: string
  name: string
  hostname: string
  os: string
  arch: string
  agent_version: string
  directories: AgentDirectory[]
  last_seen_at?: string | null
  online: boolean
  registered_at: string
  revoked_at?: string | null
}

/** Returned once, on creation. `token` is not recoverable afterwards. */
export interface AgentRegistrationToken {
  id: string
  tenant_id: string
  token: string
  suggested_name?: string
  expires_at: string
}

interface Envelope<T> {
  data: T
}

export async function listAgents(): Promise<Agent[]> {
  const resp = await apiClient.get<Envelope<Agent[]>>('/api/v1/agents')
  return resp.data.data ?? []
}

export async function createRegistrationToken(suggestedName?: string): Promise<AgentRegistrationToken> {
  const resp = await apiClient.post<Envelope<AgentRegistrationToken>>(
    '/api/v1/agents/registration-tokens',
    suggestedName ? { suggested_name: suggestedName } : {},
  )
  return resp.data.data
}

export async function renameAgent(id: string, name: string): Promise<Agent> {
  const resp = await apiClient.patch<Envelope<Agent>>(`/api/v1/agents/${encodeURIComponent(id)}`, { name })
  return resp.data.data
}

export async function revokeAgent(id: string): Promise<void> {
  await apiClient.delete(`/api/v1/agents/${encodeURIComponent(id)}`)
}

/** The command a user runs on the machine to register it. */
export function registerCommand(origin: string, token: string): string {
  return `vrsky-agent register --url ${origin} --token ${token}`
}
