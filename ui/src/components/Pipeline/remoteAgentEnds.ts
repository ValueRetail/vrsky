/**
 * Which ends of a pipeline are remote agents (#266), for the builder's Remote
 * Agent tab and deployment bar.
 */

/** One end of a pipeline that is a remote agent. */
export interface RemoteAgentEnd {
  role: 'source' | 'destination'
  agentId: string
  agentName: string
  directory: string
}

type NodeConfig = Record<string, unknown> | undefined

function endOf(role: RemoteAgentEnd['role'], config: NodeConfig): RemoteAgentEnd | null {
  if (config?.type !== 'remote_agent') return null
  const c = (config.remote_agent as Record<string, unknown>) || {}
  if (!c.agent_id) return null
  return {
    role,
    agentId: String(c.agent_id),
    agentName: String(c.agent_name || c.agent_id),
    directory: String(c.directory || ''),
  }
}

/** The remote-agent ends of a pipeline, source first; empty when neither is. */
export function remoteAgentEnds(consumer: NodeConfig, producer: NodeConfig): RemoteAgentEnd[] {
  return [endOf('source', consumer), endOf('destination', producer)].filter(
    (e): e is RemoteAgentEnd => e !== null,
  )
}

/** The deployment bar's detail for a remote-agent node: `agent:folder`. */
export function remoteAgentDetail(config: NodeConfig): string {
  const end = endOf('source', config)
  return end ? `${end.agentName}:${end.directory}` : ''
}
