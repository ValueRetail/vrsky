import { useEffect, useState, type ReactNode } from 'react'
import { listAgents, type Agent } from '../../services/agentService'
import { StyledInput, StyledSelect } from './StyledFields'

type ConnEditorProps = {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
  nodeType: string
}

// Remote agent (#266): a folder on another machine, reached through the agent
// installed there. Both directions: an input watches one of the agent's read
// folders, an output writes into one of its write folders. The folders are
// defined in the agent's own config file — this only chooses among the names it
// reported, never a path.
export default function RemoteAgentConfigEditor({ config, setConfig, nodeType }: ConnEditorProps) {
  const c = (config.remote_agent as Record<string, unknown>) || {}
  const update = (patch: Record<string, unknown>) =>
    setConfig({ ...config, remote_agent: { ...c, ...patch } })
  const [agents, setAgents] = useState<Agent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    listAgents()
      .then((a) => { if (!cancelled) setAgents(a) })
      .catch((e) => { if (!cancelled) setError(e instanceof Error ? e.message : 'Could not load agents') })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [])

  const isInput = nodeType === 'input'
  const mode = isInput ? 'read' : 'write'
  const agentId = (c.agent_id as string) || ''
  const selected = agents.find((a) => a.id === agentId)
  const live = agents.filter((a) => !a.revoked_at)
  const folders = (selected?.directories ?? []).filter((d) => d.mode === mode)
  const note = (bg: string, fg: string, text: ReactNode) => (
    <div style={{ padding: '8px 10px', background: bg, color: fg, fontSize: '11px', borderRadius: '6px', marginBottom: '12px' }}>
      {text}
    </div>
  )

  if (loading) return <p style={{ fontSize: '12px', color: '#6b7280' }}>Loading agents…</p>
  if (error) return note('#fef2f2', '#991b1b', error)
  if (live.length === 0 && !agentId) {
    return note('#eff6ff', '#1e3a8a', <>No remote agents yet. Register one under <a href="/settings/agents">Settings → Remote agents</a>, then come back here.</>)
  }

  return (
    <div>
      <StyledSelect
        label="Agent"
        value={agentId}
        onChange={(v) => {
          const a = agents.find((x) => x.id === v)
          update({ agent_id: v, agent_name: a?.name ?? '', directory: '' })
        }}
        options={[
          { value: '', label: 'Select an agent...' },
          ...live.map((a) => ({ value: a.id, label: `${a.name} · ${a.online ? 'online' : 'offline'}` })),
        ]}
      />
      {agentId && !selected && note('#fef2f2', '#991b1b',
        `The agent this node used (${(c.agent_name as string) || agentId}) is no longer registered in this workspace. Choose another.`)}
      {selected?.revoked_at && note('#fef2f2', '#991b1b',
        `${selected.name} has been revoked. Register the machine again and choose the new agent.`)}

      {selected && !selected.revoked_at && (
        folders.length === 0
          ? note('#fef3c7', '#78350f',
            `${selected.name} has no ${isInput ? 'read' : 'write'} folders. Add one to the directories section of the agent's config file, then restart the agent.`)
          : (
            <StyledSelect
              label={isInput ? 'Folder to watch' : 'Folder to write into'}
              value={(c.directory as string) || ''}
              onChange={(v) => update({ directory: v })}
              options={[
                { value: '', label: 'Select a folder...' },
                ...folders.map((d) => ({ value: d.name, label: d.name })),
              ]}
            />
          )
      )}

      {selected && !selected.revoked_at && isInput && (
        <StyledSelect
          label="After a file is taken"
          value={(c.after as string) || 'move'}
          onChange={(v) => update({ after: v })}
          options={[
            { value: 'move', label: 'Move it into processed/' },
            { value: 'delete', label: 'Delete it' },
          ]}
        />
      )}
      {selected && !selected.revoked_at && !isInput && (
        <StyledInput
          label="Filename pattern (optional)"
          placeholder="Keeps the incoming name — or e.g. orders-{timestamp}.{extension}"
          value={(c.filename_pattern as string) || ''}
          onChange={(v) => update({ filename_pattern: v })}
        />
      )}

      {selected && !selected.revoked_at && !selected.online && note('#f3f4f6', '#374151', isInput
        ? `${selected.name} is offline. Nothing is picked up until it reconnects.`
        : `${selected.name} is offline. Files wait in VRSky and are written when it reconnects — for up to 72 hours (24 for files over 256 KB).`)}
    </div>
  )
}
