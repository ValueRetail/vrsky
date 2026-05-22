/**
 * Webhook signature verification UI — Phase 1B (#67).
 *
 * Sits inside the HTTP consumer config block. Lets the user pick a provider
 * preset (GitHub, Stripe, GitLab, Shopify) or "Custom" and stores the result
 * under config.http.signature in the connector node.
 *
 * The shared secret is delegated to SecretInput so the plaintext never lands
 * in connection JSON.
 */

import SecretInput from './SecretInput'

interface SignatureBlock {
  header?: string
  algorithm?: string
  encoding?: string
  prefix?: string
  secret_id?: string
}

// Preset configurations for the most common webhook providers. The user can
// still flip to "Custom" if their provider sits outside this list.
//
// Stripe's real signature scheme is `t=…,v1=…` — a real Stripe integration
// would need timestamp validation and signed_payload reconstruction. The
// preset here covers the v1 hash only; we ship a TODO for a follow-up that
// adds dedicated Stripe support.
const PRESETS: Record<string, Omit<SignatureBlock, 'secret_id'>> = {
  GitHub: {
    header: 'X-Hub-Signature-256',
    algorithm: 'hmac-sha256',
    encoding: 'hex',
    prefix: 'sha256=',
  },
  GitLab: {
    header: 'X-Gitlab-Token',
    algorithm: 'hmac-sha256',
    encoding: 'hex',
    prefix: '',
  },
  Shopify: {
    header: 'X-Shopify-Hmac-SHA256',
    algorithm: 'hmac-sha256',
    encoding: 'base64',
    prefix: '',
  },
  Stripe: {
    header: 'Stripe-Signature',
    algorithm: 'hmac-sha256',
    encoding: 'hex',
    prefix: '',
  },
}

interface WebhookSignatureConfigProps {
  /** The config.http object (parent passes a slice of the full config). */
  http: Record<string, unknown>
  /** Patches the parent's http object. */
  onChange: (next: Record<string, unknown>) => void
}

function whichPreset(sig: SignatureBlock | undefined): string {
  if (!sig || !sig.header) return 'None'
  for (const [name, preset] of Object.entries(PRESETS)) {
    if (
      preset.header === sig.header &&
      preset.algorithm === sig.algorithm &&
      preset.encoding === sig.encoding &&
      (preset.prefix || '') === (sig.prefix || '')
    ) {
      return name
    }
  }
  return 'Custom'
}

const inputStyle: React.CSSProperties = {
  flex: 1,
  padding: '7px 10px',
  border: '1px solid #d1d5db',
  borderRadius: '6px',
  fontSize: '13px',
}

export default function WebhookSignatureConfig({ http, onChange }: WebhookSignatureConfigProps) {
  const sig = (http.signature as SignatureBlock | undefined) || undefined
  const preset = whichPreset(sig)

  const setSig = (next: SignatureBlock | undefined) => {
    const { signature: _, ...rest } = http
    if (next) {
      onChange({ ...rest, signature: next })
    } else {
      onChange(rest)
    }
  }

  const choosePreset = (name: string) => {
    if (name === 'None') {
      setSig(undefined)
      return
    }
    if (name === 'Custom') {
      setSig({
        header: sig?.header || 'X-Signature',
        algorithm: sig?.algorithm || 'hmac-sha256',
        encoding: sig?.encoding || 'hex',
        prefix: sig?.prefix || '',
        secret_id: sig?.secret_id,
      })
      return
    }
    const p = PRESETS[name]
    setSig({ ...p, secret_id: sig?.secret_id })
  }

  return (
    <div
      style={{
        marginTop: '12px',
        padding: '10px',
        border: '1px solid #e5e7eb',
        borderRadius: '6px',
        background: '#f9fafb',
      }}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '8px' }}>
        <strong style={{ fontSize: '12px', color: '#374151' }}>Signature verification (HMAC)</strong>
        <select
          value={preset}
          onChange={(e) => choosePreset(e.target.value)}
          style={{ ...inputStyle, flex: 'unset', width: '140px', padding: '4px 8px', fontSize: '12px' }}
        >
          <option value="None">None — accept all</option>
          {Object.keys(PRESETS).map((p) => (
            <option key={p} value={p}>{p}</option>
          ))}
          <option value="Custom">Custom</option>
        </select>
      </div>

      {!sig && (
        <div style={{ fontSize: '11px', color: '#6b7280' }}>
          Without a signature configured, the webhook accepts any payload. Add a provider preset for production
          use.
        </div>
      )}

      {sig && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
          {preset === 'Custom' ? (
            <>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
                <label style={{ display: 'flex', flexDirection: 'column', gap: '4px', fontSize: '11px', color: '#374151' }}>
                  Header
                  <input
                    style={inputStyle}
                    value={sig.header || ''}
                    placeholder="X-Signature"
                    onChange={(e) => setSig({ ...sig, header: e.target.value })}
                  />
                </label>
                <label style={{ display: 'flex', flexDirection: 'column', gap: '4px', fontSize: '11px', color: '#374151' }}>
                  Algorithm
                  <select
                    style={inputStyle}
                    value={sig.algorithm || 'hmac-sha256'}
                    onChange={(e) => setSig({ ...sig, algorithm: e.target.value })}
                  >
                    <option value="hmac-sha1">hmac-sha1</option>
                    <option value="hmac-sha256">hmac-sha256</option>
                    <option value="hmac-sha512">hmac-sha512</option>
                  </select>
                </label>
              </div>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
                <label style={{ display: 'flex', flexDirection: 'column', gap: '4px', fontSize: '11px', color: '#374151' }}>
                  Encoding
                  <select
                    style={inputStyle}
                    value={sig.encoding || 'hex'}
                    onChange={(e) => setSig({ ...sig, encoding: e.target.value })}
                  >
                    <option value="hex">hex</option>
                    <option value="base64">base64</option>
                  </select>
                </label>
                <label style={{ display: 'flex', flexDirection: 'column', gap: '4px', fontSize: '11px', color: '#374151' }}>
                  Header prefix
                  <input
                    style={inputStyle}
                    value={sig.prefix || ''}
                    placeholder="sha256="
                    onChange={(e) => setSig({ ...sig, prefix: e.target.value })}
                  />
                </label>
              </div>
            </>
          ) : (
            <div style={{ fontSize: '11px', color: '#6b7280', fontFamily: 'monospace' }}>
              Header: <code>{sig.header}</code> &nbsp;·&nbsp;
              {sig.algorithm} &nbsp;·&nbsp;
              {sig.encoding}
              {sig.prefix ? <> &nbsp;·&nbsp; prefix <code>{sig.prefix}</code></> : null}
            </div>
          )}

          <SecretInput
            label="Signing secret"
            placeholder="Provider's webhook signing key"
            field="secret"
            config={sig as unknown as Record<string, unknown>}
            defaultSecretName={`webhook-sig-${preset.toLowerCase()}`}
            onChange={(patch) => {
              const next: SignatureBlock = { ...sig }
              for (const [k, v] of Object.entries(patch)) {
                if (v === undefined) delete (next as Record<string, unknown>)[k]
                else (next as Record<string, unknown>)[k] = v
              }
              setSig(next)
            }}
          />
        </div>
      )}
    </div>
  )
}
