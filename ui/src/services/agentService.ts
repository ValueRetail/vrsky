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
  /** Groups this agent is in (all-tills, store-oslo, …); a node can target a group. */
  groups: string[]
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
  /** Joined by the agent that registers with this token (unless it passes --groups). */
  suggested_groups?: string[]
  expires_at: string
}

/** One group name across the workspace's live agents. */
export interface AgentGroup {
  name: string
  members: number
  online: number
}

interface Envelope<T> {
  data: T
}

export async function listAgents(): Promise<Agent[]> {
  const resp = await apiClient.get<Envelope<Agent[]>>('/api/v1/agents')
  return resp.data.data ?? []
}

export async function createRegistrationToken(suggestedName?: string, groups?: string[]): Promise<AgentRegistrationToken> {
  const body: Record<string, unknown> = {}
  if (suggestedName) body.suggested_name = suggestedName
  if (groups && groups.length > 0) body.suggested_groups = groups
  const resp = await apiClient.post<Envelope<AgentRegistrationToken>>('/api/v1/agents/registration-tokens', body)
  return resp.data.data
}

/** PATCH: a field left out is left alone; `groups: []` clears the groups. */
export async function updateAgent(id: string, patch: { name?: string; groups?: string[] }): Promise<Agent> {
  const resp = await apiClient.patch<Envelope<Agent>>(`/api/v1/agents/${encodeURIComponent(id)}`, patch)
  return resp.data.data
}

export async function renameAgent(id: string, name: string): Promise<Agent> {
  return updateAgent(id, { name })
}

export async function listAgentGroups(): Promise<AgentGroup[]> {
  const resp = await apiClient.get<Envelope<AgentGroup[]>>('/api/v1/agents/groups')
  return resp.data.data ?? []
}

/** "all-tills, store-oslo" → ["all-tills", "store-oslo"]; blanks and repeats dropped. */
export function parseGroups(text: string): string[] {
  const out: string[] = []
  for (const g of text.split(/[,\s]+/)) {
    const t = g.trim()
    if (t && !out.includes(t)) out.push(t)
  }
  return out
}

/** Ask a running pipeline's source to send everything again (e.g. to seed a
 *  till that just joined a group). Sources that support it, such as Business
 *  Central, do so on their next poll. */
export async function resendConnection(connectionId: string): Promise<void> {
  await apiClient.post(`/api/v1/connections/${encodeURIComponent(connectionId)}/resend`, {})
}

export async function revokeAgent(id: string): Promise<void> {
  await apiClient.delete(`/api/v1/agents/${encodeURIComponent(id)}`)
}

/** The command a user runs on a machine that already has the agent installed. */
export function registerCommand(origin: string, token: string): string {
  return `vrsky-agent register --url ${origin} --token ${token}`
}

/** The agent build this server offers for download (public route). */
export interface AgentRelease {
  platform: string
  version: string
  sha256: string
  size_bytes: number
  filename: string
}

export async function getAgentRelease(): Promise<AgentRelease> {
  const resp = await apiClient.get<Envelope<AgentRelease>>('/api/v1/agents/release')
  return resp.data.data
}

export function agentDownloadUrl(origin: string): string {
  return `${origin}/api/v1/agents/download/windows-amd64`
}

/**
 * The one command that sets up a Windows machine: fetches install.ps1 from
 * this server and runs it with the token. Windows PowerShell 5.1 syntax; the
 * script block form passes arguments and needs no execution-policy change.
 */
export function installCommand(origin: string, token: string): string {
  return `& ([scriptblock]::Create((irm ${origin}/api/v1/agents/install.ps1))) -Url ${origin} -Token ${token}`
}

export function uninstallCommand(origin: string): string {
  return `& ([scriptblock]::Create((irm ${origin}/api/v1/agents/uninstall.ps1)))`
}
