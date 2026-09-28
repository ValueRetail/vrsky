/**
 * Which ends of a pipeline are remote agents (#266), for the builder's Remote
 * Agent tab and deployment bar. An end is one agent or a group of agents.
 */

/** One end of a pipeline that is a remote agent (or a group of them). */
export interface RemoteAgentEnd {
  role: 'source' | 'destination'
  /** The agent's id; empty for a group. */
  agentId: string
  /** The agent's display name, or the group's name. */
  agentName: string
  /** Set when the end is a group. */
  group?: string
  directory: string
}

type NodeConfig = Record<string, unknown> | undefined

function endOf(role: RemoteAgentEnd['role'], config: NodeConfig): RemoteAgentEnd | null {
  if (config?.type !== 'remote_agent') return null
  const c = (config.remote_agent as Record<string, unknown>) || {}
  const directory = String(c.directory || '')
  if (c.target === 'group') {
    if (!c.group) return null
    return { role, agentId: '', agentName: String(c.group), group: String(c.group), directory }
  }
  if (!c.agent_id) return null
  return {
    role,
    agentId: String(c.agent_id),
    agentName: String(c.agent_name || c.agent_id),
    directory,
  }
}

/** The remote-agent ends of a pipeline, source first; empty when neither is. */
export function remoteAgentEnds(consumer: NodeConfig, producer: NodeConfig): RemoteAgentEnd[] {
  return [endOf('source', consumer), endOf('destination', producer)].filter(
    (e): e is RemoteAgentEnd => e !== null,
  )
}

/** The deployment bar's detail for a remote-agent node: `agent:folder` or `group:folder`. */
export function remoteAgentDetail(config: NodeConfig): string {
  const end = endOf('source', config)
  return end ? `${end.agentName}:${end.directory}` : ''
}
