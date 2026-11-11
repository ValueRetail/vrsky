import { useState } from 'react'
import SecretInput from './SecretInput'
import TestConnectionButton from './TestConnectionButton'
import { StyledInput, StyledSelect } from './StyledFields'

// NatsConfigEditor (#814): the source settings for an external NATS JetStream,
// such as a Retailhero partner account. Stores config.nats = {servers[],
// auth_type, username, password, creds, nkey_seed, ca_cert, stream, durable,
// filter_subject, create_durable, api_prefix, inbox_prefix, batch_size,
// max_wait_seconds}. password, creds and nkey_seed are minted into the
// secrets vault on deploy (SECRET_FIELDS); the connector refuses them in
// plaintext.

const labelStyle: React.CSSProperties = {
  fontSize: '12px', fontWeight: 600, color: '#374151', display: 'block', marginBottom: '6px',
  textTransform: 'uppercase', letterSpacing: '0.025em',
}

const areaStyle: React.CSSProperties = {
  width: '100%', minHeight: '72px', padding: '8px 10px', border: '1px solid #d1d5db',
  borderRadius: '4px', fontSize: '12px', fontFamily: 'monospace', color: '#111827', boxSizing: 'border-box',
}

const hintStyle: React.CSSProperties = { fontSize: '11px', color: '#6b7280' }

export default function NatsConfigEditor({
  config,
  setConfig,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
}) {
  const nats = (config.nats as Record<string, unknown>) || {}
  const update = (patch: Record<string, unknown>) =>
    setConfig({ ...config, nats: { ...nats, ...patch } })

  const servers = Array.isArray(nats.servers) ? (nats.servers as string[]) : []
  // The box keeps what was typed; the list is parsed from it. Rebuilding the
  // text from the list on every keystroke would eat the line break (or comma)
  // before a second server could be typed.
  const [serversText, setServersText] = useState(() => servers.join('\n'))
  const authType = (nats.auth_type as string) || 'none'
  const deliverPolicy = (nats.deliver_policy as string) || 'all'
  // A .creds file is several lines, which a password box would flatten, so it
  // gets a text area. Saved = a secret is bound and nothing new was pasted.
  const credsSaved = Boolean(nats.creds_secret_id) && !nats.creds
  // Unset means VRSky may create the durable, as before the switch existed.
  const createDurable = nats.create_durable !== false
  // Whole numbers only: the connector reads these as integers, and a
  // fraction would fail the whole block.
  const numberOrUndefined = (v: string) => {
    const n = Number.parseInt(v, 10)
    return Number.isNaN(n) ? undefined : n
  }

  return (
    <div className="space-y-3">
      <div style={{ marginBottom: '16px' }}>
        <label style={labelStyle} htmlFor="nats-servers">Server URLs (one per line)</label>
        <textarea
          id="nats-servers"
          style={{ ...areaStyle, minHeight: '48px' }}
          spellCheck={false}
          placeholder="tls://nats.example.com:4222"
          value={serversText}
          onChange={(e) => {
            setServersText(e.target.value)
            update({ servers: e.target.value.split(/[\n,]/).map((s) => s.trim()).filter(Boolean) })
          }}
        />
      </div>
      <StyledInput
        label="Stream"
        placeholder="RH_EVENTS"
        value={(nats.stream as string) || ''}
        onChange={(v) => update({ stream: v })}
      />
      <StyledInput
        label="Durable consumer name"
        placeholder="vrsky"
        value={(nats.durable as string) || ''}
        onChange={(v) => update({ durable: v })}
      />
      <StyledInput
        label="Subject filter (optional)"
        placeholder="rh.evt.v1.acme.>"
        value={(nats.filter_subject as string) || ''}
        onChange={(v) => update({ filter_subject: v })}
      />
      <label style={{ ...hintStyle, display: 'flex', gap: '6px', alignItems: 'center', marginBottom: '16px' }}>
        <input
          type="checkbox"
          checked={createDurable}
          onChange={(e) => update({ create_durable: e.target.checked })}
        />
        Create the durable on the first start if it is missing
      </label>

      {/* deliver_policy only shapes a durable VRSky creates. */}
      {createDurable && (
        <>
          <StyledSelect
            label="First start reads"
            value={deliverPolicy}
            onChange={(v) => update({ deliver_policy: v, opt_start_time: v === 'by_start_time' ? nats.opt_start_time : undefined })}
            options={[
              { value: 'all', label: 'The whole stream' },
              { value: 'new', label: 'Only new messages' },
              { value: 'by_start_time', label: 'From a point in time' },
            ]}
          />
          {deliverPolicy === 'by_start_time' && (
            <StyledInput
              label="Start time (RFC 3339)"
              placeholder="2026-11-01T00:00:00Z"
              value={(nats.opt_start_time as string) || ''}
              onChange={(v) => update({ opt_start_time: v })}
            />
          )}
          <div style={{ ...hintStyle, marginTop: '-8px', marginBottom: '12px' }}>
            Applies only when VRSky creates the durable. &quot;The whole stream&quot; sends every message the stream
            holds into the pipeline on the first start.
          </div>
        </>
      )}

      <StyledSelect
        label="Authentication"
        value={authType}
        onChange={(v) => update({ auth_type: v })}
        options={[
          { value: 'none', label: 'None' },
          { value: 'creds', label: 'Credentials file (JWT + NKey)' },
          { value: 'user_password', label: 'User and password' },
          { value: 'nkey', label: 'NKey seed' },
        ]}
      />

      {authType === 'user_password' && (
        <>
          <StyledInput
            label="Username"
            value={(nats.username as string) || ''}
            onChange={(v) => update({ username: v })}
          />
          <SecretInput
            label="Password"
            placeholder="NATS password"
            field="password"
            config={nats}
            defaultSecretName="nats-password"
            onChange={(patch) => update(patch)}
          />
        </>
      )}

      {authType === 'creds' && (
        <div>
          <label style={labelStyle} htmlFor="nats-creds">Credentials file (.creds)</label>
          <textarea
            id="nats-creds"
            style={areaStyle}
            spellCheck={false}
            autoComplete="off"
            placeholder={credsSaved ? 'Saved — paste a new file to replace it' : '-----BEGIN NATS USER JWT-----'}
            value={(nats.creds as string) || ''}
            onChange={(e) => update({ creds: e.target.value, creds_secret_id: undefined })}
          />
          <div style={{ ...hintStyle, color: credsSaved ? '#15803d' : '#6b7280' }}>
            {credsSaved
              ? 'Saved to the encrypted secrets vault.'
              : 'Stored encrypted (as a tenant secret) when you deploy.'}
          </div>
        </div>
      )}

      {authType === 'nkey' && (
        <SecretInput
          label="NKey seed"
          placeholder="SU…"
          field="nkey_seed"
          config={nats}
          defaultSecretName="nats-nkey-seed"
          onChange={(patch) => update(patch)}
        />
      )}

      <StyledInput
        label="JetStream API prefix (optional)"
        placeholder="JS.RH.API"
        value={(nats.api_prefix as string) || ''}
        onChange={(v) => update({ api_prefix: v })}
      />
      <StyledInput
        label="Inbox prefix (optional)"
        placeholder="_INBOX_vrsky_t_a"
        value={(nats.inbox_prefix as string) || ''}
        onChange={(v) => update({ inbox_prefix: v })}
      />
      <div style={{ ...hintStyle, marginTop: '-8px', marginBottom: '12px' }}>
        Set both for an account that imports the JetStream API from another account and may only use its own reply
        inboxes, such as a Retailhero partner account.
      </div>

      <StyledInput
        label="Batch size (optional)"
        type="number"
        min={1}
        max={256}
        step={1}
        placeholder="10"
        value={nats.batch_size === undefined ? '' : String(nats.batch_size)}
        onChange={(v) => update({ batch_size: numberOrUndefined(v) })}
      />
      <StyledInput
        label="Pull wait in seconds (optional)"
        type="number"
        min={1}
        max={30}
        step={1}
        placeholder="5"
        value={nats.max_wait_seconds === undefined ? '' : String(nats.max_wait_seconds)}
        onChange={(v) => update({ max_wait_seconds: numberOrUndefined(v) })}
      />

      <div>
        <label style={labelStyle} htmlFor="nats-ca">CA certificate (PEM, optional)</label>
        <textarea
          id="nats-ca"
          style={areaStyle}
          spellCheck={false}
          placeholder="-----BEGIN CERTIFICATE-----"
          value={(nats.ca_cert as string) || ''}
          onChange={(e) => update({ ca_cert: e.target.value })}
        />
      </div>

      <div style={hintStyle}>
        A durable pull consumer with explicit ack. Each message is acked on the NATS server only after it is in the
        pipeline, and its Nats-Msg-Id keeps a redelivery from being published twice. TLS is required. VRSky never changes
        an existing durable.
      </div>
      <TestConnectionButton config={config} role="consumer" />
    </div>
  )
}
