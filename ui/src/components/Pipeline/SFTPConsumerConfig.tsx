import { useEffect } from 'react'
import SecretInput from './SecretInput'
import TestConnectionButton from './TestConnectionButton'
import { StyledInput, StyledSelect } from './StyledFields'
import { DEFAULT_POLL_INTERVAL_SECONDS } from './pollInterval'

// SFTPConsumerConfig (#76): watch a remote SFTP directory, fetch + publish new
// files, and apply an after-action. Stores config.sftp = {host, port, username,
// password, private_key, host_key, remote_dir, file_pattern,
// poll_interval_seconds, after_action, move_dir}. The password / private key are
// minted into the secrets vault on deploy (SECRET_FIELDS).
export default function SFTPConsumerConfig({
  config,
  setConfig,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
}) {
  const sftp = (config.sftp as Record<string, unknown>) || {}
  const update = (patch: Record<string, unknown>) =>
    setConfig({ ...config, sftp: { ...sftp, ...patch } })

  const labelStyle: React.CSSProperties = {
    fontSize: '12px', fontWeight: 500, color: '#374151', display: 'block', marginBottom: '4px',
  }
  const afterAction = (sftp.after_action as string) || 'none'

  // Save the 60 the field shows (#589). The consumer reads a missing
  // poll_interval_seconds as 0, "poll once", so a node saved without touching
  // the field would poll once and stop. Keyed on the field being unset, not on
  // mount: the editor is not remounted when the selection moves to another
  // SFTP node.
  const pollUnset = sftp.poll_interval_seconds === undefined
  useEffect(() => {
    if (pollUnset) update({ poll_interval_seconds: DEFAULT_POLL_INTERVAL_SECONDS })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pollUnset])

  return (
    <div className="space-y-3">
      <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr', gap: '8px' }}>
        <StyledInput
          label="Host"
          placeholder="sftp.example.com"
          value={(sftp.host as string) || ''}
          onChange={(v) => update({ host: v })}
        />
        <StyledInput
          label="Port"
          type="number"
          placeholder="22"
          value={String((sftp.port as number) ?? 22)}
          onChange={(v) => update({ port: parseInt(v) || 22 })}
        />
      </div>

      <StyledInput
        label="Username"
        placeholder="vrsky"
        value={(sftp.username as string) || ''}
        onChange={(v) => update({ username: v })}
      />

      <div>
        <SecretInput
          label="Password"
          placeholder="SFTP password (or use a private key below)"
          field="password"
          config={sftp}
          defaultSecretName="sftp-password"
          onChange={(patch) => update(patch)}
        />
      </div>

      <div>
        <div style={labelStyle}>Private key (PEM, optional — use instead of password)</div>
        <textarea
          aria-label="Private key (PEM)"
          style={{
            width: '100%', minHeight: '70px', padding: '8px 10px',
            border: '1px solid #d1d5db', borderRadius: '4px', fontSize: '12px',
            fontFamily: 'monospace', color: '#111827', boxSizing: 'border-box',
          }}
          placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
          value={(sftp.private_key as string) || ''}
          onChange={(e) => update({ private_key: e.target.value })}
        />
      </div>

      <div>
        <div style={labelStyle}>Host key (optional — pin the server's key to verify its identity)</div>
        <textarea
          aria-label="Host key"
          style={{
            width: '100%', minHeight: '50px', padding: '8px 10px',
            border: '1px solid #d1d5db', borderRadius: '4px', fontSize: '12px',
            fontFamily: 'monospace', color: '#111827', boxSizing: 'border-box',
          }}
          placeholder="ssh-ed25519 AAAA…  (a known_hosts / authorized_keys line)"
          value={(sftp.host_key as string) || ''}
          onChange={(e) => update({ host_key: e.target.value })}
        />
        <div style={{ fontSize: '11px', color: '#b45309', marginTop: '2px' }}>
          Leave empty to skip host-key verification (dev only). Pin it in production.
        </div>
      </div>

      <StyledInput
        label="Remote directory"
        placeholder="/upload"
        value={(sftp.remote_dir as string) || ''}
        onChange={(v) => update({ remote_dir: v })}
      />
      <StyledInput
        label="File pattern (optional)"
        placeholder="*.csv"
        value={(sftp.file_pattern as string) || ''}
        onChange={(v) => update({ file_pattern: v })}
      />

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
        <StyledInput
          label="Poll interval (s, 0 = once)"
          type="number"
          placeholder="60"
          value={String((sftp.poll_interval_seconds as number) ?? DEFAULT_POLL_INTERVAL_SECONDS)}
          onChange={(v) => update({ poll_interval_seconds: parseInt(v) || 0 })}
        />
        <StyledSelect
          label="After action"
          value={afterAction}
          onChange={(v) => update({ after_action: v })}
          options={[
            { value: 'none', label: 'Leave in place' },
            { value: 'delete', label: 'Delete' },
            { value: 'move', label: 'Move' },
          ]}
        />
      </div>

      {afterAction === 'move' && (
        <StyledInput
          label="Move to directory"
          placeholder="/upload/processed"
          value={(sftp.move_dir as string) || ''}
          onChange={(v) => update({ move_dir: v })}
        />
      )}

      <div style={{ fontSize: '11px', color: '#6b7280' }}>
        The password / private key is stored encrypted in the secrets vault on deploy.
        The pipeline polls the remote directory and ingests each new file.
      </div>
      <TestConnectionButton config={config} role="consumer" />
    </div>
  )
}
