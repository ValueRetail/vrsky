/**
 * What an admin copies to set up a Windows machine (#266).
 *
 * The primary command fetches install.ps1 from this server and runs it: it
 * downloads the agent, asks for the folders, registers with the token and
 * starts the service. The manual `vrsky-agent register` line stays for a
 * machine that already has the agent. The token appears once in each, as an
 * argument, so a doubled paste fails clearly instead of sending the wrong
 * thing as the token.
 */

import { useEffect, useState } from 'react'
import {
  agentDownloadUrl, getAgentRelease, installCommand, registerCommand, uninstallCommand,
  type AgentRelease,
} from '../../services/agentService'

const code: React.CSSProperties = {
  flex: '1 1 420px', padding: '6px 8px', background: '#fff', border: '1px solid #fde68a',
  borderRadius: '4px', wordBreak: 'break-all', fontSize: '12px',
}
const button: React.CSSProperties = {
  padding: '6px 12px', fontSize: '12px', border: '1px solid #d1d5db', borderRadius: '4px',
  background: '#fff', cursor: 'pointer',
}

function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      type="button"
      onClick={() => {
        navigator.clipboard?.writeText(text)
        setCopied(true)
        setTimeout(() => setCopied(false), 1500)
      }}
      style={{ ...button, background: '#2563eb', color: '#fff', border: 'none' }}
    >
      {copied ? 'Copied' : label}
    </button>
  )
}

interface Props {
  origin: string
  token: string
  onDone: () => void
}

export default function InstallCommand({ origin, token, onDone }: Props) {
  const [release, setRelease] = useState<AgentRelease | 'unavailable' | null>(null)
  useEffect(() => {
    let cancelled = false
    getAgentRelease()
      .then((r) => { if (!cancelled) setRelease(r) })
      .catch(() => { if (!cancelled) setRelease('unavailable') })
    return () => { cancelled = true }
  }, [])

  const oneLiner = installCommand(origin, token)
  const manual = registerCommand(origin, token)

  return (
    <div data-testid="install-command">
      <div style={{ fontWeight: 600, marginBottom: '6px' }}>
        On the Windows machine, open PowerShell as Administrator and paste this. It downloads the agent, asks
        which folders to use, registers the machine and starts the service.
      </div>
      <div style={{ display: 'flex', gap: '8px', alignItems: 'center', flexWrap: 'wrap' }}>
        <code style={code}>{oneLiner}</code>
        <CopyButton text={oneLiner} />
        <button type="button" onClick={onDone} style={button}>Done</button>
      </div>
      <div style={{ marginTop: '6px', color: '#92400e' }}>
        The token works once and expires in an hour; it will not be shown again. Anyone who has it can register
        a machine in this workspace until then.
      </div>

      <details style={{ marginTop: '10px' }}>
        <summary style={{ cursor: 'pointer' }}>Other ways</summary>
        <div style={{ marginTop: '8px', display: 'grid', gap: '8px' }}>
          <div>
            <a href={agentDownloadUrl(origin)} style={{ color: '#1d4ed8' }}>Download vrsky-agent.exe</a>
            {release && release !== 'unavailable' && (
              <span style={{ color: '#6b7280' }}> · version {release.version}</span>
            )}
            {release === 'unavailable' && (
              <span style={{ color: '#991b1b' }}> · not available: this server was built without the agent</span>
            )}
          </div>
          <div>
            <div style={{ marginBottom: '4px' }}>Agent already installed on the machine? Register it with:</div>
            <div style={{ display: 'flex', gap: '8px', alignItems: 'center', flexWrap: 'wrap' }}>
              <code style={code}>{manual}</code>
              <CopyButton text={manual} />
            </div>
          </div>
        </div>
      </details>
    </div>
  )
}

/** The one-liner that removes the agent from a machine. */
export function UninstallCommand({ origin }: { origin: string }) {
  const cmd = uninstallCommand(origin)
  return (
    <details style={{ fontSize: '12px', color: '#6b7280', marginTop: '16px' }} data-testid="uninstall-command">
      <summary style={{ cursor: 'pointer' }}>Remove the agent from a machine</summary>
      <div style={{ marginTop: '6px' }}>
        In PowerShell as Administrator. Keeps the machine's config and registration; add <code>-Purge</code> to
        delete those too. Revoke the agent here afterwards.
      </div>
      <div style={{ display: 'flex', gap: '8px', alignItems: 'center', flexWrap: 'wrap', marginTop: '6px' }}>
        <code style={{ ...code, border: '1px solid #e5e7eb', background: '#f9fafb' }}>{cmd}</code>
        <CopyButton text={cmd} />
      </div>
    </details>
  )
}
