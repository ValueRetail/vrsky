/**
 * SecretInput — a normal masked credential field that is stored encrypted.
 *
 * It behaves like an ordinary login password box: you type a value and it is
 * held (masked) in the node config, so live actions like "Test Connection" can
 * use it immediately. At **deploy** time the pipeline builder sweeps the config,
 * mints a tenant secret for every plaintext credential, and replaces it with a
 * `<field>_secret_id` reference (see `materializeSecrets` in PipelineBuilder) —
 * so nothing plaintext is ever persisted server-side. The worker resolves the
 * `_secret_id` back to plaintext at runtime via the secrets store (#66).
 *
 * Props pattern: the parent owns a config object holding either
 *   - { [field]: "<plaintext being typed>" }                (pre-deploy)
 *   - { [field + "_secret_id"]: "<uuid>" }                  (deployed / saved)
 */

import { useState } from 'react'

interface SecretInputProps {
  label: string
  placeholder?: string
  /** Plain field name (e.g. "password"). The stored secret key is field + "_secret_id". */
  field: string
  /** The current config record holding either field or field+"_secret_id". */
  config: Record<string, unknown>
  /** Suggested secret name (used as a naming hint when minted at deploy). Optional. */
  defaultSecretName?: string
  /** Updates the parent's config object with a partial patch. */
  onChange: (patch: Record<string, unknown>) => void
}

const inputStyle: React.CSSProperties = {
  flex: 1,
  padding: '7px 10px',
  border: '1px solid #d1d5db',
  borderRadius: '6px',
  fontSize: '13px',
  fontFamily: 'inherit',
  color: '#111827',
  background: '#fff',
}

const revealButtonStyle: React.CSSProperties = {
  padding: '7px 10px',
  borderRadius: '6px',
  fontSize: '12px',
  fontWeight: 600,
  cursor: 'pointer',
  border: '1px solid #d1d5db',
  background: '#f3f4f6',
  color: '#374151',
}

export default function SecretInput({
  label,
  placeholder,
  field,
  config,
  onChange,
}: SecretInputProps) {
  const secretIDKey = field + '_secret_id'
  const value = (config[field] as string) || ''
  // "Saved" = a secret is already bound and the user hasn't started typing a
  // replacement. In that state the box is empty with a hint; typing replaces it.
  const isSaved = Boolean(config[secretIDKey]) && value === ''

  const [reveal, setReveal] = useState(false)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
      <label style={{ fontSize: '12px', fontWeight: 500, color: '#374151' }}>{label}</label>
      <div style={{ display: 'flex', gap: '6px', alignItems: 'center' }}>
        <input
          type={reveal ? 'text' : 'password'}
          style={inputStyle}
          value={value}
          placeholder={isSaved ? '•••••••• saved — type to replace' : placeholder || 'Enter value'}
          autoComplete="new-password"
          // Typing sets plaintext and drops any previously-bound secret id so a
          // fresh secret is minted on the next deploy.
          onChange={(e) => onChange({ [field]: e.target.value, [secretIDKey]: undefined })}
        />
        <button
          type="button"
          onClick={() => setReveal((r) => !r)}
          style={revealButtonStyle}
          aria-label={reveal ? 'Hide value' : 'Show value'}
        >
          {reveal ? 'Hide' : 'Show'}
        </button>
      </div>
      <div style={{ fontSize: '11px', color: isSaved ? '#15803d' : '#6b7280' }}>
        {isSaved
          ? '🔒 Saved to the encrypted secrets vault.'
          : '🔒 Stored encrypted (as a tenant secret) when you deploy.'}
      </div>
    </div>
  )
}
