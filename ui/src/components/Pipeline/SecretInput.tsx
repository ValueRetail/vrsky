/**
 * SecretInput — masked input bound to the tenant secrets store.
 *
 * Props pattern: the parent owns a config object with either
 *   - { [field]: "<plaintext>" }   (legacy, pre-migration)
 *   - { [field + "_secret_id"]: "<uuid>" }  (post-migration)
 *
 * On "Save", the component:
 *   1. POSTs the new plaintext to /api/v1/secrets, receives a UUID.
 *   2. Calls onChange({ [field]: undefined, [field + "_secret_id"]: uuid }).
 *
 * For a row already bound to a secret_id, the value reads as "●●●●●●" and the
 * only action is "Replace".
 *
 * For legacy plaintext, a small banner offers a one-click "Migrate to secret"
 * that does the same POST and rewires the config.
 */

import { useState } from 'react'
import { createSecret } from '../../services/secretService'

interface SecretInputProps {
  label: string
  placeholder?: string
  /** Plain field name (e.g. "password"). The secret id key is field + "_secret_id". */
  field: string
  /** The current config record holding either field or field+"_secret_id". */
  config: Record<string, unknown>
  /** Suggested secret name when minting a new one. Should be unique per tenant. */
  defaultSecretName: string
  /** Updates the parent's config object. Receives a partial patch. */
  onChange: (patch: Record<string, unknown>) => void
}

const inputStyle: React.CSSProperties = {
  flex: 1,
  padding: '7px 10px',
  border: '1px solid #d1d5db',
  borderRadius: '6px',
  fontSize: '13px',
  fontFamily: 'inherit',
}

const buttonStyle: React.CSSProperties = {
  padding: '7px 12px',
  borderRadius: '6px',
  fontSize: '12px',
  fontWeight: 600,
  cursor: 'pointer',
  border: '1px solid transparent',
}

export default function SecretInput({
  label,
  placeholder,
  field,
  config,
  defaultSecretName,
  onChange,
}: SecretInputProps) {
  const secretIDKey = field + '_secret_id'
  const secretID = (config[secretIDKey] as string) || ''
  const plaintext = (config[field] as string) || ''
  const isBound = secretID !== ''
  const isPlaintext = !isBound && plaintext !== ''

  const [editing, setEditing] = useState(false)
  const [draftName, setDraftName] = useState(defaultSecretName)
  const [draftValue, setDraftValue] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const save = async () => {
    if (!draftName.trim() || !draftValue) {
      setError('Name and value are both required')
      return
    }
    setBusy(true)
    setError(null)
    try {
      const s = await createSecret(draftName.trim(), draftValue)
      // Wire the new secret in and drop any old plaintext key.
      onChange({ [field]: undefined, [secretIDKey]: s.id })
      setEditing(false)
      setDraftValue('')
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to save secret')
    } finally {
      setBusy(false)
    }
  }

  // Form for entering / replacing a secret.
  if (editing) {
    return (
      <div style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
        <label style={{ fontSize: '12px', fontWeight: 500, color: '#374151' }}>{label}</label>
        <input
          type="text"
          style={inputStyle}
          placeholder="Secret name (e.g. prod-pg-pwd)"
          value={draftName}
          onChange={(e) => setDraftName(e.target.value)}
        />
        <input
          type="password"
          style={inputStyle}
          placeholder={placeholder || 'New secret value'}
          value={draftValue}
          onChange={(e) => setDraftValue(e.target.value)}
          autoFocus
        />
        {error && <div style={{ color: '#dc2626', fontSize: '12px' }}>{error}</div>}
        <div style={{ display: 'flex', gap: '6px' }}>
          <button
            disabled={busy}
            onClick={save}
            style={{ ...buttonStyle, background: '#2563eb', color: '#fff' }}
          >
            {busy ? 'Saving…' : 'Save secret'}
          </button>
          <button
            disabled={busy}
            onClick={() => {
              setEditing(false)
              setDraftValue('')
              setError(null)
            }}
            style={{ ...buttonStyle, background: '#f3f4f6', color: '#374151', borderColor: '#d1d5db' }}
          >
            Cancel
          </button>
        </div>
      </div>
    )
  }

  // Steady state — either bound to a secret or holding legacy plaintext.
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
      <label style={{ fontSize: '12px', fontWeight: 500, color: '#374151' }}>{label}</label>
      <div style={{ display: 'flex', gap: '6px', alignItems: 'center' }}>
        <input
          type="text"
          style={{ ...inputStyle, fontFamily: 'monospace', color: '#6b7280' }}
          value={isBound ? '••••••••' : isPlaintext ? '•• legacy plaintext ••' : ''}
          placeholder={placeholder || 'No secret set'}
          readOnly
        />
        <button
          onClick={() => setEditing(true)}
          style={{ ...buttonStyle, background: '#f3f4f6', color: '#374151', borderColor: '#d1d5db' }}
        >
          {isBound ? 'Replace' : 'Add secret'}
        </button>
      </div>
      {isPlaintext && (
        <div style={{ fontSize: '11px', color: '#b45309' }}>
          This value is still stored in plaintext. Click <strong>Replace</strong> to migrate it to the secrets store.
        </div>
      )}
    </div>
  )
}
