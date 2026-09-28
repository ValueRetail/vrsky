import { useEffect, useState, type ReactNode } from 'react'
import { listAgentGroups, listAgents, type Agent, type AgentGroup } from '../../services/agentService'
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
//
// A node can target ONE agent or a GROUP of agents (all-tills, store-oslo, …):
// a group output reaches every member, each with its own delivery queue; a
// group input takes files from every member's folder of that name.
export default function RemoteAgentConfigEditor({ config, setConfig, nodeType }: ConnEditorProps) {
  const c = (config.remote_agent as Record<string, unknown>) || {}
  const update = (patch: Record<string, unknown>) =>
    setConfig({ ...config, remote_agent: { ...c, ...patch } })
  const [agents, setAgents] = useState<Agent[]>([])
  const [groups, setGroups] = useState<AgentGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    listAgents()
      .then((a) => { if (!cancelled) setAgents(a) })
      .catch((e) => { if (!cancelled) setError(e instanceof Error ? e.message : 'Could not load agents') })
      .finally(() => { if (!cancelled) setLoading(false) })
    listAgentGroups()
      .then((g) => { if (!cancelled) setGroups(g) })
      .catch(() => { /* the group list is a convenience; the agents carry their groups too */ })
    return () => { cancelled = true }
  }, [])

  const isInput = nodeType === 'input'
  const mode = isInput ? 'read' : 'write'
  const target = c.target === 'group' ? 'group' : 'agent'
  const agentId = (c.agent_id as string) || ''
  const groupName = (c.group as string) || ''
  const selected = agents.find((a) => a.id === agentId)
  const live = agents.filter((a) => !a.revoked_at)
  const note = (bg: string, fg: string, text: ReactNode) => (
    <div style={{ padding: '8px 10px', background: bg, color: fg, fontSize: '11px', borderRadius: '6px', marginBottom: '12px' }}>
      {text}
    </div>
  )

  if (loading) return <p style={{ fontSize: '12px', color: '#6b7280' }}>Loading agents…</p>
  if (error) return note('#fef2f2', '#991b1b', error)
  if (live.length === 0 && !agentId && target === 'agent') {
    return note('#eff6ff', '#1e3a8a', <>No remote agents yet. Register one under <a href="/settings/agents">Settings → Remote agents</a>, then come back here.</>)
  }

  // Group mode: the folders are what the members report; a folder not every
  // member has is still offered (the others are skipped, and named in the
  // Remote Agent tab), but marked.
  const members = groupName ? live.filter((a) => (a.groups ?? []).includes(groupName)) : []
  const groupFolders = (() => {
    const count: Record<string, number> = {}
    for (const a of members) for (const d of a.directories) if (d.mode === mode) count[d.name] = (count[d.name] ?? 0) + 1
    return Object.entries(count).sort(([x], [y]) => x.localeCompare(y)).map(([name, n]) => ({ name, n }))
  })()
  const groupNames = Array.from(new Set([...groups.map((g) => g.name), ...live.flatMap((a) => a.groups ?? [])])).sort()
  const groupLabel = (name: string) => {
    const g = groups.find((x) => x.name === name)
    const m = g?.members ?? live.filter((a) => (a.groups ?? []).includes(name)).length
    const on = g?.online ?? live.filter((a) => (a.groups ?? []).includes(name) && a.online).length
    return `${name} · ${m} agent${m === 1 ? '' : 's'} · ${on} online`
  }

  const folders = (selected?.directories ?? []).filter((d) => d.mode === mode)
  const ready = target === 'group' ? Boolean(groupName) : Boolean(selected && !selected.revoked_at)

  return (
    <div>
      <StyledSelect
        label={isInput ? 'Read from' : 'Send to'}
        value={target}
        onChange={(v) => (v === 'group'
          ? update({ target: 'group', agent_id: undefined, agent_name: undefined, directory: '' })
          : update({ target: undefined, group: undefined, directory: '' }))}
        options={[
          { value: 'agent', label: 'One agent' },
          { value: 'group', label: 'A group of agents' },
        ]}
      />

      {target === 'group' && (
        groupNames.length === 0
          ? note('#eff6ff', '#1e3a8a', <>No groups yet. Put agents in a group under <a href="/settings/agents">Settings → Remote agents</a> (or with <code>-Groups</code> when installing), then come back here.</>)
          : (
            <StyledSelect
              label="Group"
              value={groupName}
              onChange={(v) => update({ group: v, directory: '' })}
              options={[
                { value: '', label: 'Select a group...' },
                ...groupNames.map((name) => ({ value: name, label: groupLabel(name) })),
              ]}
            />
          )
      )}
      {target === 'group' && groupName && !groupNames.includes(groupName) && note('#fef2f2', '#991b1b',
        `No agent is in group "${groupName}" any more. Choose another group, or add agents to it in Settings.`)}
      {target === 'group' && groupName && members.length > 0 && (
        groupFolders.length === 0
          ? note('#fef3c7', '#78350f',
            `No agent in ${groupName} has a ${mode} folder. Add one to the directories section of the agents' config files, then restart them.`)
          : (
            <StyledSelect
              label={isInput ? 'Folder to watch' : 'Folder to write into'}
              value={(c.directory as string) || ''}
              onChange={(v) => update({ directory: v })}
              options={[
                { value: '', label: 'Select a folder...' },
                ...groupFolders.map((f) => ({
                  value: f.name,
                  label: f.n === members.length ? f.name : `${f.name} · only ${f.n} of ${members.length} agents have it`,
                })),
              ]}
            />
          )
      )}

      {target === 'agent' && (
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
      )}
      {target === 'agent' && agentId && !selected && note('#fef2f2', '#991b1b',
        `The agent this node used (${(c.agent_name as string) || agentId}) is no longer registered in this workspace. Choose another.`)}
      {target === 'agent' && selected?.revoked_at && note('#fef2f2', '#991b1b',
        `${selected.name} has been revoked. Register the machine again and choose the new agent.`)}

      {target === 'agent' && selected && !selected.revoked_at && (
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

      {ready && isInput && (
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
      {ready && !isInput && (
        <StyledInput
          label="Filename pattern (optional)"
          placeholder="Keeps the incoming name — or e.g. orders-{timestamp}.{extension}"
          value={(c.filename_pattern as string) || ''}
          onChange={(v) => update({ filename_pattern: v })}
        />
      )}

      {target === 'agent' && selected && !selected.revoked_at && !selected.online && note('#f3f4f6', '#374151', isInput
        ? `${selected.name} is offline. Nothing is picked up until it reconnects.`
        : `${selected.name} is offline. Files wait in VRSky and are written when it reconnects — for up to 72 hours (24 for files over 256 KB).`)}
      {target === 'group' && groupName && members.length > 0 && members.some((a) => !a.online) && note('#f3f4f6', '#374151',
        `${members.filter((a) => !a.online).map((a) => a.name).join(', ')} ${members.filter((a) => !a.online).length === 1 ? 'is' : 'are'} offline. ` +
        (isInput ? 'Their files are picked up when they reconnect.' : 'Files for them wait in VRSky (72 hours, 24 for files over 256 KB); the others are not held up.'))}
    </div>
  )
}
