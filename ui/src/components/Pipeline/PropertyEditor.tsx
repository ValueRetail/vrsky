import { useState, useEffect, useRef } from 'react'
import type { Node, Edge } from '../../types/pipeline'
import SecretInput from './SecretInput'
import WebhookSignatureConfig from './WebhookSignatureConfig'
import OAuthGrantSelector from './OAuthGrantSelector'
import { useAuthStore } from '../../store/authStore'
import * as tenantDataService from '../../services/tenantDataService'
import type { TenantDataConnection, DataConnectionRequest } from '../../types/models'

// Walk upstream from a node via edges, returning the first ancestor that's
// an 'input' (consumer). Returns undefined if none reachable.
//
// Precomputes a node lookup and an incoming-edge adjacency map, then walks an
// index-based queue, keeping the traversal O(V + E) rather than O(V * E).
function findUpstreamConsumer(nodeId: string, nodes: Node[], edges: Edge[]): Node | undefined {
  const nodeById = new Map(nodes.map(n => [n.id, n]))
  const sourcesByTarget = new Map<string, string[]>()
  for (const edge of edges) {
    const list = sourcesByTarget.get(edge.target)
    if (list) list.push(edge.source)
    else sourcesByTarget.set(edge.target, [edge.source])
  }

  const visited = new Set<string>()
  const queue = [nodeId]
  let head = 0
  while (head < queue.length) {
    const current = queue[head++]
    if (visited.has(current)) continue
    visited.add(current)
    for (const sourceId of sourcesByTarget.get(current) || []) {
      const src = nodeById.get(sourceId)
      if (!src) continue
      if (src.type === 'input') return src
      queue.push(src.id)
    }
  }
  return undefined
}

// Type for API Consumer endpoint
interface ApiEndpoint {
  path: string
  params: string
  auth_type: 'none' | 'bearer' | 'api_key' | 'oauth'
  auth_value: string
  // Set when auth_type === 'oauth': the OAuth grant whose access token the
  // worker injects as a Bearer header (resolved at request time in PR #3).
  oauth_grant_id?: string
}

// Muted color palette matching ComponentPalette
const NODE_COLORS: Record<string, { bg: string; hoverBg: string; text: string }> = {
  consumer: { bg: '#93c5fd', hoverBg: '#7cb3f0', text: '#1e3a5f' },
  filter: { bg: '#fdba74', hoverBg: '#f0a85e', text: '#5c3d1e' },
  converter: { bg: '#f9a8d4', hoverBg: '#f08ec2', text: '#5c1e3d' },
  producer: { bg: '#86efac', hoverBg: '#6de095', text: '#1e5c3a' },
}

// Reusable styled input component
function StyledInput({
  label,
  placeholder,
  value,
  onChange,
  type = 'text',
}: {
  label: string
  placeholder?: string
  value: string
  onChange: (value: string) => void
  type?: string
}) {
  const [focused, setFocused] = useState(false)

  return (
    <div style={{ marginBottom: '16px' }}>
      <label
        style={{
          display: 'block',
          fontSize: '12px',
          fontWeight: 600,
          color: '#374151',
          marginBottom: '6px',
          textTransform: 'uppercase',
          letterSpacing: '0.025em',
        }}
      >
        {label}
      </label>
      <input
        type={type}
        placeholder={placeholder}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onFocus={() => setFocused(true)}
        onBlur={() => setFocused(false)}
        style={{
          width: '100%',
          padding: '10px 12px',
          border: focused ? '1px solid #3b82f6' : '1px solid #d1d5db',
          borderRadius: '6px',
          backgroundColor: '#ffffff',
          color: '#1f2937',
          fontSize: '13px',
          outline: 'none',
          transition: 'all 150ms ease',
          boxShadow: focused ? '0 0 0 3px rgba(59, 130, 246, 0.1)' : 'none',
          boxSizing: 'border-box',
        }}
      />
    </div>
  )
}

// Reusable styled select component
function StyledSelect({
  label,
  value,
  onChange,
  options,
}: {
  label: string
  value: string
  onChange: (value: string) => void
  options: { value: string; label: string }[]
}) {
  const [focused, setFocused] = useState(false)

  return (
    <div style={{ marginBottom: '16px' }}>
      <label
        style={{
          display: 'block',
          fontSize: '12px',
          fontWeight: 600,
          color: '#374151',
          marginBottom: '6px',
          textTransform: 'uppercase',
          letterSpacing: '0.025em',
        }}
      >
        {label}
      </label>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onFocus={() => setFocused(true)}
        onBlur={() => setFocused(false)}
        style={{
          width: '100%',
          padding: '10px 12px',
          border: focused ? '1px solid #3b82f6' : '1px solid #d1d5db',
          borderRadius: '6px',
          backgroundColor: '#ffffff',
          color: '#1f2937',
          fontSize: '13px',
          outline: 'none',
          transition: 'all 150ms ease',
          boxShadow: focused ? '0 0 0 3px rgba(59, 130, 246, 0.1)' : 'none',
          boxSizing: 'border-box',
          cursor: 'pointer',
        }}
      >
        {options.map((opt) => (
          <option key={opt.value} value={opt.value}>
            {opt.label}
          </option>
        ))}
      </select>
    </div>
  )
}

// Styled button component with hover effects
function StyledButton({
  children,
  onClick,
  variant = 'primary',
  disabled = false,
}: {
  children: React.ReactNode
  onClick: () => void
  variant?: 'primary' | 'danger'
  disabled?: boolean
}) {
  const [hovered, setHovered] = useState(false)

  const colors = {
    primary: {
      bg: '#86efac',
      hoverBg: '#6de095',
      text: '#1e5c3a',
    },
    danger: {
      bg: '#fca5a5',
      hoverBg: '#f87171',
      text: '#7f1d1d',
    },
    disabled: {
      bg: '#e5e7eb',
      hoverBg: '#e5e7eb',
      text: '#9ca3af',
    },
  }

  const colorSet = disabled ? colors.disabled : colors[variant]

  return (
    <button
      onClick={disabled ? undefined : onClick}
      onMouseEnter={() => !disabled && setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      disabled={disabled}
      style={{
        width: '100%',
        padding: '10px 16px',
        backgroundColor: hovered && !disabled ? colorSet.hoverBg : colorSet.bg,
        color: colorSet.text,
        border: '1px solid rgba(0, 0, 0, 0.08)',
        borderRadius: '6px',
        fontSize: '13px',
        fontWeight: 500,
        cursor: disabled ? 'not-allowed' : 'pointer',
        transition: 'all 150ms ease',
        transform: hovered && !disabled ? 'scale(1.02)' : 'scale(1)',
        boxShadow: hovered && !disabled ? '0 2px 4px rgba(0, 0, 0, 0.1)' : '0 1px 2px rgba(0, 0, 0, 0.05)',
        opacity: disabled ? 0.7 : 1,
      }}
    >
      {children}
    </button>
  )
}

// Tenant Consumer configuration component — inline connection management
function TenantConsumerConfig({
  config,
  setConfig,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
}) {
  const { currentTenant } = useAuthStore()
  const [connections, setConnections] = useState<TenantDataConnection[]>([])
  const [pendingRequests, setPendingRequests] = useState<DataConnectionRequest[]>([])
  const [loading, setLoading] = useState(true)
  const [view, setView] = useState<'main' | 'create'>('main')
  const [targetTenantId, setTargetTenantId] = useState('')
  const [permissionType, setPermissionType] = useState('both')
  const [message, setMessage] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [sharedConns, setSharedConns] = useState<{ id: string; name: string }[]>([])
  const [loadingShared, setLoadingShared] = useState(false)

  const loadData = () => {
    if (!currentTenant) { setLoading(false); return }
    setLoading(true)
    Promise.all([
      tenantDataService.listDataConnections(currentTenant.id),
      tenantDataService.listOutgoingRequests(currentTenant.id),
    ])
      .then(([conns, reqs]) => {
        setConnections(conns.filter(c => c.status === 'active'))
        setPendingRequests(reqs.filter(r => r.status === 'pending'))
      })
      .catch(() => { setConnections([]); setPendingRequests([]) })
      .finally(() => setLoading(false))
  }

  useEffect(() => { loadData() }, [currentTenant]) // eslint-disable-line react-hooks/exhaustive-deps

  const tenant = (config.tenant as Record<string, unknown>) || {}
  const selectedDataConnectionId = (tenant.connection_id as string) || ''

  // Load shared connections when a data connection is selected
  useEffect(() => {
    if (!currentTenant || !selectedDataConnectionId) {
      setSharedConns([])
      return
    }
    setLoadingShared(true)
    tenantDataService.getSharedConnections(currentTenant.id, selectedDataConnectionId)
      .then(setSharedConns)
      .catch(() => setSharedConns([]))
      .finally(() => setLoadingShared(false))
  }, [currentTenant, selectedDataConnectionId])

  const handleSubmitRequest = async () => {
    if (!currentTenant || !targetTenantId.trim()) return
    setSubmitting(true)
    setError('')
    try {
      await tenantDataService.createConnectionRequest(currentTenant.id, {
        target_api_key: targetTenantId.trim(),
        permission_type: permissionType,
        message: message || undefined,
      })
      setView('main')
      setTargetTenantId('')
      setMessage('')
      loadData()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to send request')
    } finally {
      setSubmitting(false)
    }
  }

  if (loading) {
    return <p style={{ fontSize: '13px', color: '#9ca3af', marginTop: '16px' }}>Loading...</p>
  }

  if (view === 'create') {
    return (
      <div style={{ marginTop: '16px' }}>
        <p style={{ fontSize: '13px', fontWeight: 600, color: '#374151', marginBottom: '12px' }}>Request New Connection</p>
        <StyledInput
          label="Target API Key"
          placeholder="Paste the other workspace's API key"
          value={targetTenantId}
          onChange={setTargetTenantId}
        />
        <StyledSelect
          label="Permission Type"
          value={permissionType}
          onChange={setPermissionType}
          options={[
            { value: 'both', label: 'Both (send & receive)' },
            { value: 'send', label: 'Send only' },
            { value: 'receive', label: 'Receive only' },
          ]}
        />
        <StyledInput
          label="Message (optional)"
          placeholder="Why are you requesting this connection?"
          value={message}
          onChange={setMessage}
        />
        {error && (
          <p style={{ fontSize: '12px', color: '#dc2626', margin: '8px 0' }}>{error}</p>
        )}
        <div style={{ display: 'flex', gap: '8px', marginTop: '12px' }}>
          <button
            onClick={handleSubmitRequest}
            disabled={!targetTenantId.trim() || submitting}
            style={{
              padding: '8px 16px', fontSize: '13px', fontWeight: 500,
              backgroundColor: submitting ? '#9ca3af' : '#2563eb', color: '#fff',
              border: 'none', borderRadius: '6px', cursor: submitting ? 'default' : 'pointer',
            }}
          >
            {submitting ? 'Sending...' : 'Send Request'}
          </button>
          <button
            onClick={() => { setView('main'); setError('') }}
            style={{
              padding: '8px 16px', fontSize: '13px', fontWeight: 500,
              backgroundColor: '#f3f4f6', color: '#374151',
              border: '1px solid #d1d5db', borderRadius: '6px', cursor: 'pointer',
            }}
          >
            Cancel
          </button>
        </div>
      </div>
    )
  }

  return (
    <div style={{ marginTop: '16px' }}>
      {/* Active connections dropdown */}
      {connections.length > 0 && (
        <>
          <StyledSelect
            label="Data Connection"
            value={selectedDataConnectionId}
            onChange={(value) => {
              const selected = connections.find(c => c.id === value)
              const sourceTenantId = selected
                ? (selected.requester_tenant_id === currentTenant?.id ? selected.target_tenant_id : selected.requester_tenant_id)
                : ''
              setConfig({ ...config, tenant: { ...tenant, connection_id: value, source_tenant_id: sourceTenantId, source_connection_id: '' } })
            }}
            options={[
              { value: '', label: 'Select a data connection...' },
              ...connections.map(c => ({
                value: c.id,
                label: `${c.requester_tenant_id === currentTenant?.id ? 'To' : 'From'}: ${c.requester_tenant_id === currentTenant?.id ? c.target_tenant_id.slice(0, 8) : c.requester_tenant_id.slice(0, 8)}... (${c.permission_type})`,
              })),
            ]}
          />

          {/* Shared pipeline picker */}
          {selectedDataConnectionId && (
            loadingShared ? (
              <p style={{ fontSize: '12px', color: '#9ca3af', marginTop: '8px' }}>Loading shared pipelines...</p>
            ) : sharedConns.length > 0 ? (
              <StyledSelect
                label="Source Pipeline"
                value={(tenant.source_connection_id as string) || ''}
                onChange={(value) => setConfig({ ...config, tenant: { ...tenant, source_connection_id: value } })}
                options={[
                  { value: '', label: 'All shared pipelines' },
                  ...sharedConns.map(sc => ({ value: sc.id, label: sc.name })),
                ]}
              />
            ) : (
              <p style={{ fontSize: '12px', color: '#9ca3af', marginTop: '8px' }}>No specific pipelines shared — will receive all data from this connection.</p>
            )
          )}
        </>
      )}

      {/* Pending outgoing requests */}
      {pendingRequests.length > 0 && (
        <div style={{ marginTop: '12px' }}>
          <p style={{ fontSize: '11px', fontWeight: 600, color: '#6b7280', textTransform: 'uppercase', marginBottom: '6px' }}>Pending Requests</p>
          {pendingRequests.map(req => (
            <div key={req.id} style={{
              padding: '8px 12px', marginBottom: '4px',
              backgroundColor: '#fffbeb', border: '1px solid #fde68a', borderRadius: '6px',
              fontSize: '12px', color: '#92400e',
            }}>
              To: {req.target_tenant_id.slice(0, 8)}... — <span style={{ fontWeight: 600 }}>{req.status}</span>
              {req.message && <span style={{ color: '#b45309' }}> · {req.message}</span>}
            </div>
          ))}
        </div>
      )}

      {/* Empty state or new request button */}
      {connections.length === 0 && pendingRequests.length === 0 && (
        <div style={{ padding: '16px', backgroundColor: '#f9fafb', borderRadius: '6px', border: '1px solid #e5e7eb', textAlign: 'center' }}>
          <p style={{ fontSize: '13px', color: '#6b7280', margin: '0 0 8px' }}>No active data connections yet.</p>
        </div>
      )}

      <div style={{ display: 'flex', gap: '8px', marginTop: '12px' }}>
        <button
          onClick={() => setView('create')}
          style={{
            padding: '8px 16px', fontSize: '13px', fontWeight: 500,
            backgroundColor: '#2563eb', color: '#fff',
            border: 'none', borderRadius: '6px', cursor: 'pointer',
          }}
        >
          Request New Connection
        </button>
        <button
          onClick={loadData}
          style={{
            padding: '8px 16px', fontSize: '13px', fontWeight: 500,
            backgroundColor: '#f3f4f6', color: '#374151',
            border: '1px solid #d1d5db', borderRadius: '6px', cursor: 'pointer',
          }}
        >
          Refresh
        </button>
      </div>
    </div>
  )
}

// Salesforce producer config (#79 PR 2): write records via REST (insert/upsert),
// auto Bulk API for batches ≥200. Stores config.salesforce = {instance_url,
// oauth_grant_id, object, operation, external_id_field, api_version}.
function SalesforceProducerConfig({
  config,
  setConfig,
  deployedConnectionId,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
  deployedConnectionId?: string
}) {
  const sf = (config.salesforce as Record<string, unknown>) || {}
  const update = (patch: Record<string, unknown>) =>
    setConfig({ ...config, salesforce: { ...sf, ...patch } })
  const operation = (sf.operation as string) || 'insert'
  const labelStyle: React.CSSProperties = {
    fontSize: '12px', fontWeight: 500, color: '#374151', display: 'block', marginBottom: '4px',
  }

  return (
    <div className="space-y-3">
      <StyledInput
        label="Instance URL"
        placeholder="https://your-org.my.salesforce.com"
        value={(sf.instance_url as string) || ''}
        onChange={(v) => update({ instance_url: v })}
      />
      <div>
        <label style={labelStyle}>Salesforce account (OAuth)</label>
        <OAuthGrantSelector
          value={(sf.oauth_grant_id as string) || ''}
          onChange={(grantId) => update({ oauth_grant_id: grantId || '' })}
          connectionId={deployedConnectionId}
        />
      </div>
      <StyledInput
        label="Object (sObject)"
        placeholder="Account"
        value={(sf.object as string) || ''}
        onChange={(v) => update({ object: v })}
      />
      <StyledSelect
        label="Operation"
        value={operation}
        onChange={(v) => update({ operation: v })}
        options={[
          { value: 'insert', label: 'Insert' },
          { value: 'upsert', label: 'Upsert (by external id)' },
        ]}
      />
      {operation === 'upsert' && (
        <StyledInput
          label="External ID field"
          placeholder="ExternalId__c"
          value={(sf.external_id_field as string) || ''}
          onChange={(v) => update({ external_id_field: v })}
        />
      )}
      <StyledInput
        label="API version"
        placeholder="v60.0"
        value={(sf.api_version as string) || ''}
        onChange={(v) => update({ api_version: v })}
      />
      <div style={{ fontSize: '11px', color: '#6b7280' }}>
        Records flowing into this node are written to Salesforce. Batches of 200+ records
        automatically use the Bulk API 2.0.
      </div>
    </div>
  )
}

// Salesforce consumer config (#79 PR 1): SOQL poll authenticated by an OAuth
// grant. Stores config.salesforce = {instance_url, oauth_grant_id, soql,
// poll_interval_seconds, api_version}.
function SalesforceConsumerConfig({
  config,
  setConfig,
  deployedConnectionId,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
  deployedConnectionId?: string
}) {
  const sf = (config.salesforce as Record<string, unknown>) || {}
  const update = (patch: Record<string, unknown>) =>
    setConfig({ ...config, salesforce: { ...sf, ...patch } })

  const labelStyle: React.CSSProperties = {
    fontSize: '12px', fontWeight: 500, color: '#374151', display: 'block', marginBottom: '4px',
  }

  return (
    <div className="space-y-3">
      <StyledInput
        label="Instance URL"
        placeholder="https://your-org.my.salesforce.com"
        value={(sf.instance_url as string) || ''}
        onChange={(v) => update({ instance_url: v })}
      />

      <div>
        <label style={labelStyle}>Salesforce account (OAuth)</label>
        <OAuthGrantSelector
          value={(sf.oauth_grant_id as string) || ''}
          onChange={(grantId) => update({ oauth_grant_id: grantId || '' })}
          connectionId={deployedConnectionId}
        />
      </div>

      <div>
        <label style={labelStyle}>SOQL query</label>
        <textarea
          style={{
            width: '100%', minHeight: '70px', padding: '8px 10px',
            border: '1px solid #d1d5db', borderRadius: '4px', fontSize: '12px',
            fontFamily: 'monospace', color: '#111827', boxSizing: 'border-box',
          }}
          placeholder="SELECT Id, Name FROM Account ORDER BY LastModifiedDate DESC"
          value={(sf.soql as string) || ''}
          onChange={(e) => update({ soql: e.target.value })}
        />
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
        <StyledInput
          label="Poll interval (s, 0 = once)"
          placeholder="0"
          type="number"
          value={String((sf.poll_interval_seconds as number) ?? 0)}
          onChange={(v) => update({ poll_interval_seconds: parseInt(v) || 0 })}
        />
        <StyledInput
          label="API version"
          placeholder="v60.0"
          value={(sf.api_version as string) || ''}
          onChange={(v) => update({ api_version: v })}
        />
      </div>
      <div style={{ fontSize: '11px', color: '#6b7280' }}>
        Connect a Salesforce account (OAuth) above, then enter a SOQL query. For a
        sandbox, register a Salesforce provider with the test.salesforce.com URLs.
      </div>
    </div>
  )
}

// SFTPConsumerConfig (#76): watch a remote SFTP directory, fetch + publish new
// files, and apply an after-action. Stores config.sftp = {host, port, username,
// password, private_key, host_key, remote_dir, file_pattern,
// poll_interval_seconds, after_action, move_dir}. The password / private key are
// minted into the secrets vault on deploy (SECRET_FIELDS).
function SFTPConsumerConfig({
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
        <label style={labelStyle}>Password</label>
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
        <label style={labelStyle}>Private key (PEM, optional — use instead of password)</label>
        <textarea
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
        <label style={labelStyle}>Host key (optional — pin the server's key to verify its identity)</label>
        <textarea
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
          value={String((sftp.poll_interval_seconds as number) ?? 60)}
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
    </div>
  )
}

// SFTPProducerConfig (#76): upload each pipeline message as a file to a remote
// SFTP directory, named from a template. Stores config.sftp = {host, port,
// username, password, private_key, host_key, remote_dir, filename_template}.
// The password / private key is minted into the secrets vault on deploy.
function SFTPProducerConfig({
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
        <label style={labelStyle}>Password</label>
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
        <label style={labelStyle}>Private key (PEM, optional — use instead of password)</label>
        <textarea
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
        <label style={labelStyle}>Host key (optional — pin the server's key to verify its identity)</label>
        <textarea
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
        label="Filename template"
        placeholder="order_{{.id}}_{{.timestamp}}.json"
        value={(sftp.filename_template as string) || ''}
        onChange={(v) => update({ filename_template: v })}
      />

      <div style={{ fontSize: '11px', color: '#6b7280' }}>
        Each message is uploaded as a file. The filename template can reference
        payload fields (e.g. <code>{'{{.id}}'}</code>) plus <code>{'{{.timestamp}}'}</code> and{' '}
        <code>{'{{.uuid}}'}</code>. Defaults to <code>{'{{.uuid}}'}.json</code>.
      </div>
    </div>
  )
}

// KafkaConfigEditor (#77): shared config form for the Kafka consumer + producer.
// Stores config.kafka = {brokers[], topic, consumer_group?, auth_type, username,
// password, ca_cert, client_cert, client_key}. password + client_key are minted
// into the secrets vault on deploy. role switches the consumer-only field
// (consumer_group) and the producer-only hint (acks=all).
function KafkaConfigEditor({
  config,
  setConfig,
  role,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
  role: 'consumer' | 'producer'
}) {
  const kafka = (config.kafka as Record<string, unknown>) || {}
  const update = (patch: Record<string, unknown>) =>
    setConfig({ ...config, kafka: { ...kafka, ...patch } })

  const labelStyle: React.CSSProperties = {
    fontSize: '12px', fontWeight: 500, color: '#374151', display: 'block', marginBottom: '4px',
  }
  const authType = (kafka.auth_type as string) || 'none'
  const isSASL = authType.startsWith('sasl')
  const isMTLS = authType === 'mtls'
  const brokers = Array.isArray(kafka.brokers) ? (kafka.brokers as string[]) : []

  const certArea = (label: string, field: string, placeholder: string) => (
    <div>
      <label style={labelStyle}>{label}</label>
      <textarea
        style={{
          width: '100%', minHeight: '60px', padding: '8px 10px',
          border: '1px solid #d1d5db', borderRadius: '4px', fontSize: '12px',
          fontFamily: 'monospace', color: '#111827', boxSizing: 'border-box',
        }}
        placeholder={placeholder}
        value={(kafka[field] as string) || ''}
        onChange={(e) => update({ [field]: e.target.value })}
      />
    </div>
  )

  return (
    <div className="space-y-3">
      <StyledInput
        label="Brokers (comma-separated)"
        placeholder="broker1:9092, broker2:9092"
        value={brokers.join(', ')}
        onChange={(v) => update({ brokers: v.split(',').map((s) => s.trim()).filter(Boolean) })}
      />
      <StyledInput
        label="Topic"
        placeholder="orders"
        value={(kafka.topic as string) || ''}
        onChange={(v) => update({ topic: v })}
      />
      {role === 'consumer' && (
        <StyledInput
          label="Consumer group"
          placeholder="vrsky"
          value={(kafka.consumer_group as string) || ''}
          onChange={(v) => update({ consumer_group: v })}
        />
      )}

      <StyledSelect
        label="Authentication"
        value={authType}
        onChange={(v) => update({ auth_type: v })}
        options={[
          { value: 'none', label: 'None' },
          { value: 'sasl-plain', label: 'SASL / PLAIN' },
          { value: 'sasl-scram-256', label: 'SASL / SCRAM-SHA-256' },
          { value: 'sasl-scram-512', label: 'SASL / SCRAM-SHA-512' },
          { value: 'mtls', label: 'mTLS' },
        ]}
      />

      {isSASL && (
        <>
          <StyledInput
            label="Username"
            value={(kafka.username as string) || ''}
            onChange={(v) => update({ username: v })}
          />
          <div>
            <label style={labelStyle}>Password</label>
            <SecretInput
              label="Password"
              placeholder="SASL password"
              field="password"
              config={kafka}
              defaultSecretName="kafka-password"
              onChange={(patch) => update(patch)}
            />
          </div>
        </>
      )}

      {(isMTLS || isSASL) && certArea('CA certificate (PEM, optional)', 'ca_cert', '-----BEGIN CERTIFICATE-----')}
      {isMTLS && certArea('Client certificate (PEM)', 'client_cert', '-----BEGIN CERTIFICATE-----')}
      {isMTLS && certArea('Client key (PEM)', 'client_key', '-----BEGIN PRIVATE KEY-----')}

      <div style={{ fontSize: '11px', color: '#6b7280' }}>
        {role === 'consumer'
          ? 'The group offset is committed only after a message is published into the pipeline (at-least-once).'
          : 'Messages are produced with acks=all (wait for all in-sync replicas).'}{' '}
        Password and client key are stored encrypted in the secrets vault on deploy.
      </div>
    </div>
  )
}

// RabbitMQConfigEditor (#78): shared config form for the RabbitMQ consumer +
// producer. Stores config.rabbitmq = {url, username, password, exchange,
// exchange_type, queue, routing_key}. password is minted into the secrets vault
// on deploy. role switches help text (manual-ack vs persistent publish).
function RabbitMQConfigEditor({
  config,
  setConfig,
  role,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
  role: 'consumer' | 'producer'
}) {
  const rmq = (config.rabbitmq as Record<string, unknown>) || {}
  const update = (patch: Record<string, unknown>) =>
    setConfig({ ...config, rabbitmq: { ...rmq, ...patch } })

  const labelStyle: React.CSSProperties = {
    fontSize: '12px', fontWeight: 500, color: '#374151', display: 'block', marginBottom: '4px',
  }

  return (
    <div className="space-y-3">
      <StyledInput
        label="AMQP URL"
        placeholder="amqp://rabbitmq:5672"
        value={(rmq.url as string) || ''}
        onChange={(v) => update({ url: v })}
      />
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
        <StyledInput
          label="Username (optional)"
          value={(rmq.username as string) || ''}
          onChange={(v) => update({ username: v })}
        />
        <div>
          <label style={labelStyle}>Password (optional)</label>
          <SecretInput
            label="Password"
            placeholder="AMQP password"
            field="password"
            config={rmq}
            defaultSecretName="rabbitmq-password"
            onChange={(patch) => update(patch)}
          />
        </div>
      </div>
      <StyledInput
        label="Queue"
        placeholder="orders"
        value={(rmq.queue as string) || ''}
        onChange={(v) => update({ queue: v })}
      />
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
        <StyledInput
          label="Exchange (optional)"
          placeholder="events"
          value={(rmq.exchange as string) || ''}
          onChange={(v) => update({ exchange: v })}
        />
        <StyledInput
          label="Routing key (optional)"
          placeholder="orders.created"
          value={(rmq.routing_key as string) || ''}
          onChange={(v) => update({ routing_key: v })}
        />
      </div>
      <div style={{ fontSize: '11px', color: '#6b7280' }}>
        {role === 'consumer'
          ? 'Messages are manually acked only after a successful publish into the pipeline (at-least-once). Credentials may be embedded in the URL or set above.'
          : 'Messages are published as persistent (delivery_mode=2) to the exchange (or directly to the queue if no exchange is set).'}{' '}
        The password is stored encrypted in the secrets vault on deploy.
      </div>
    </div>
  )
}

// CloudStorageConfigEditor configures the single cloud-storage connector
// (Amazon S3 / Azure Blob / GCS) for both consumer and producer roles. The
// provider picker swaps the credential fields. PR1 ships S3 (and S3-compatible
// stores such as MinIO via a custom endpoint); Azure/GCS land in PR2.
function CloudStorageConfigEditor({
  config,
  setConfig,
  role,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
  role: 'consumer' | 'producer'
}) {
  const cs = (config.cloud_storage as Record<string, unknown>) || {}
  const update = (patch: Record<string, unknown>) =>
    setConfig({ ...config, cloud_storage: { ...cs, ...patch } })

  const labelStyle: React.CSSProperties = {
    fontSize: '12px', fontWeight: 500, color: '#374151', display: 'block', marginBottom: '4px',
  }
  const provider = (cs.provider as string) || 's3'
  const afterAction = (cs.after_action as string) || 'none'
  const mode = (cs.mode as string) || 'poll'

  return (
    <div className="space-y-3">
      <StyledSelect
        label="Provider"
        value={provider}
        onChange={(v) => update({ provider: v })}
        options={[
          { value: 's3', label: 'Amazon S3 (or S3-compatible)' },
          { value: 'azure', label: 'Azure Blob Storage' },
          { value: 'gcs', label: 'Google Cloud Storage' },
        ]}
      />

      <StyledInput
        label={provider === 'azure' ? 'Container' : 'Bucket'}
        placeholder={provider === 'azure' ? 'my-container' : 'my-bucket'}
        value={(cs.bucket as string) || ''}
        onChange={(v) => update({ bucket: v })}
      />
      <StyledInput
        label="Prefix (optional)"
        placeholder={role === 'consumer' ? 'incoming/' : 'outgoing/'}
        value={(cs.prefix as string) || ''}
        onChange={(v) => update({ prefix: v })}
      />

      {provider === 's3' && (
        <>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
            <StyledInput
              label="Region"
              placeholder="us-east-1"
              value={(cs.region as string) || ''}
              onChange={(v) => update({ region: v })}
            />
            <StyledInput
              label="Endpoint (optional, for MinIO)"
              placeholder="http://minio:9000"
              value={(cs.endpoint as string) || ''}
              onChange={(v) => update({ endpoint: v })}
            />
          </div>
          <StyledInput
            label="Access key ID"
            placeholder="AKIA… (or MinIO user)"
            value={(cs.access_key_id as string) || ''}
            onChange={(v) => update({ access_key_id: v })}
          />
          <div>
            <label style={labelStyle}>Secret access key</label>
            <SecretInput
              label="Secret access key"
              placeholder="S3 secret access key"
              field="secret_access_key"
              config={cs}
              defaultSecretName="s3-secret-access-key"
              onChange={(patch) => update(patch)}
            />
          </div>
        </>
      )}

      {provider === 'azure' && (
        <>
          <div>
            <label style={labelStyle}>Connection string</label>
            <SecretInput
              label="Connection string"
              placeholder="DefaultEndpointsProtocol=…;AccountName=…;AccountKey=…"
              field="connection_string"
              config={cs}
              defaultSecretName="azure-connection-string"
              onChange={(patch) => update(patch)}
            />
            <div style={{ fontSize: '11px', color: '#6b7280', marginTop: '2px' }}>
              Easiest option (also works with the Azurite emulator). Or set the account name + key below.
            </div>
          </div>
          <StyledInput
            label="Account name (optional, instead of connection string)"
            placeholder="mystorageaccount"
            value={(cs.account_name as string) || ''}
            onChange={(v) => update({ account_name: v })}
          />
          <div>
            <label style={labelStyle}>Account key (optional)</label>
            <SecretInput
              label="Account key"
              placeholder="Azure storage account key"
              field="account_key"
              config={cs}
              defaultSecretName="azure-account-key"
              onChange={(patch) => update(patch)}
            />
          </div>
        </>
      )}

      {provider === 'gcs' && (
        <>
          <StyledInput
            label="Endpoint (optional, for a custom/private GCS endpoint)"
            placeholder="https://storage.googleapis.com/storage/v1/"
            value={(cs.endpoint as string) || ''}
            onChange={(v) => update({ endpoint: v })}
          />
          <div>
            <label style={labelStyle}>Service account JSON</label>
            <SecretInput
              label="Service account JSON"
              placeholder='{"type":"service_account", …}'
              field="credentials_json"
              config={cs}
              defaultSecretName="gcs-credentials-json"
              onChange={(patch) => update(patch)}
            />
            <div style={{ fontSize: '11px', color: '#6b7280', marginTop: '2px' }}>
              Paste the service-account key JSON. (For the local fake-gcs emulator, leave this
              blank and run the worker with <code>STORAGE_EMULATOR_HOST</code> set — an endpoint
              alone doesn't cover the GCS client's read path.)
            </div>
          </div>
        </>
      )}

      {role === 'consumer' ? (
        <>
          <StyledSelect
            label="Ingestion mode"
            value={mode}
            onChange={(v) => update({ mode: v })}
            options={[
              { value: 'poll', label: 'Poll (list the bucket on an interval)' },
              { value: 'event', label: 'Event-driven (S3 → SQS)' },
            ]}
          />

          {mode === 'event' ? (
            <>
              <StyledInput
                label="SQS queue URL"
                placeholder="https://sqs.us-east-1.amazonaws.com/123456789012/my-queue"
                value={(cs.event_queue_url as string) || ''}
                onChange={(v) => update({ event_queue_url: v })}
              />
              <StyledInput
                label="SQS endpoint override (optional, e.g. LocalStack)"
                placeholder="http://localstack:4566"
                value={(cs.event_endpoint as string) || ''}
                onChange={(v) => update({ event_endpoint: v })}
              />
              <div style={{ fontSize: '11px', color: '#6b7280' }}>
                Subscribe the bucket's notifications to this SQS queue (S3 only). Each object is
                ingested as it arrives; the message is acked only after a successful publish.
                The endpoint override is for the SQS service (distinct from the object-store
                endpoint above); leave blank for real AWS SQS. Azure Blob / GCS use poll mode.
              </div>
            </>
          ) : (
            <StyledInput
              label="File pattern (optional)"
              placeholder="*.csv"
              value={(cs.file_pattern as string) || ''}
              onChange={(v) => update({ file_pattern: v })}
            />
          )}

          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
            {mode !== 'event' && (
              <StyledInput
                label="Poll interval (s, 0 = once)"
                type="number"
                placeholder="60"
                value={String((cs.poll_interval_seconds as number) ?? 60)}
                onChange={(v) => update({ poll_interval_seconds: parseInt(v) || 0 })}
              />
            )}
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
              label="Move to prefix"
              placeholder="processed/"
              value={(cs.move_prefix as string) || ''}
              onChange={(v) => update({ move_prefix: v })}
            />
          )}
        </>
      ) : (
        <>
          <StyledInput
            label="Key template"
            placeholder="orders/{{.id}}_{{.timestamp}}.json"
            value={(cs.key_template as string) || ''}
            onChange={(v) => update({ key_template: v })}
          />
          {(() => {
            const sse = (cs.sse as Record<string, unknown>) || {}
            const sseMode = (sse.mode as string) || 'none'
            const updateSSE = (patch: Record<string, unknown>) =>
              update({ sse: { ...sse, ...patch } })
            return (
              <>
                <StyledSelect
                  label="Server-side encryption"
                  value={sseMode}
                  onChange={(v) => updateSSE({ mode: v })}
                  options={[
                    { value: 'none', label: 'Bucket default' },
                    { value: 'sse-s3', label: 'SSE-S3 (AES-256, S3 only)' },
                    { value: 'sse-kms', label: 'KMS / CMEK key' },
                  ]}
                />
                {sseMode === 'sse-kms' && (
                  <StyledInput
                    label={provider === 's3' ? 'KMS key ID/ARN' : provider === 'azure' ? 'Encryption scope name' : 'Cloud KMS key name'}
                    placeholder={provider === 's3' ? 'arn:aws:kms:…' : provider === 'azure' ? 'my-encryption-scope' : 'projects/…/cryptoKeys/…'}
                    value={(sse.kms_key_id as string) || ''}
                    onChange={(v) => updateSSE({ kms_key_id: v })}
                  />
                )}
              </>
            )
          })()}
        </>
      )}

      <div style={{ fontSize: '11px', color: '#6b7280' }}>
        {role === 'consumer'
          ? 'The bucket is polled under the prefix and each new object is ingested.'
          : 'Each message is written as an object. The key template can reference payload fields plus the timestamp and uuid built-ins; it defaults to the message id.'}{' '}
        Credentials are stored encrypted in the secrets vault on deploy.
      </div>
    </div>
  )
}

// API Consumer configuration component
// Minimal by default - just Base URL
// Advanced options (poll interval, endpoints) expandable
function DatabaseConsumerConfig({
  config,
  setConfig,
  deployedConnectionId,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
  deployedConnectionId?: string
}) {
  const [testStatus, setTestStatus] = useState<{ ok?: boolean; error?: string; tables?: string[] } | null>(null)
  const [testing, setTesting] = useState(false)

  const dbConfig = (config.database as Record<string, unknown>) || {}
  const host = (dbConfig.host as string) || ''
  const port = (dbConfig.port as number) || 5432
  const user = (dbConfig.user as string) || ''
  const password = (dbConfig.password as string) || ''
  const database = (dbConfig.database as string) || ''
  const table = (dbConfig.table as string) || ''
  const query = (dbConfig.query as string) || ''
  const pollInterval = (dbConfig.poll_interval_seconds as number) || 0

  const updateDB = (updates: Record<string, unknown>) => {
    setConfig({
      ...config,
      database: { ...dbConfig, ...updates },
    })
  }

  const testConnection = async () => {
    setTesting(true)
    setTestStatus(null)
    try {
      const resp = await fetch('http://localhost:9300/test-connection/', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ host, port, user, password, database, table }),
      })
      const data = await resp.json()
      setTestStatus(data)
    } catch (err) {
      setTestStatus({ ok: false, error: 'Cannot reach db-consumer service. Is it running?' })
    }
    setTesting(false)
  }

  return (
    <div className="space-y-3">
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 100px', gap: '8px' }}>
        <StyledInput
          label="Host"
          placeholder="localhost or db.example.com"
          value={host}
          onChange={(v) => updateDB({ host: v })}
        />
        <StyledInput
          label="Port"
          placeholder="5432"
          value={String(port)}
          onChange={(v) => updateDB({ port: parseInt(v) || 5432 })}
        />
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
        <StyledInput
          label="Username"
          placeholder="postgres"
          value={user}
          onChange={(v) => updateDB({ user: v })}
        />
        <SecretInput
          label="Password"
          placeholder="••••••••"
          field="password"
          config={dbConfig}
          defaultSecretName={`pg-pwd-${host || 'db'}`}
          onChange={(patch) => updateDB(patch)}
        />
      </div>
      <StyledInput
        label="Database"
        placeholder="my_database"
        value={database}
        onChange={(v) => updateDB({ database: v })}
      />

      <button
        onClick={testConnection}
        disabled={testing || !host || !database || !user}
        style={{
          width: '100%',
          padding: '8px 12px',
          backgroundColor: testing ? '#9ca3af' : '#2563eb',
          color: '#fff',
          border: 'none',
          borderRadius: '6px',
          fontSize: '13px',
          fontWeight: 600,
          cursor: testing ? 'not-allowed' : 'pointer',
        }}
      >
        {testing ? 'Testing...' : 'Test Connection'}
      </button>

      {testStatus && (
        <div style={{
          padding: '8px 12px',
          borderRadius: '6px',
          fontSize: '12px',
          backgroundColor: testStatus.ok ? '#f0fdf4' : '#fef2f2',
          border: `1px solid ${testStatus.ok ? '#bbf7d0' : '#fecaca'}`,
          color: testStatus.ok ? '#166534' : '#991b1b',
        }}>
          {testStatus.ok ? (
            <>
              <strong>Connected!</strong>
              {testStatus.tables && testStatus.tables.length > 0 && (
                <div style={{ marginTop: '4px' }}>
                  Tables: {testStatus.tables.join(', ')}
                </div>
              )}
            </>
          ) : (
            <>{testStatus.error}</>
          )}
        </div>
      )}

      {testStatus?.ok && testStatus.tables && testStatus.tables.length > 0 ? (
        <StyledSelect
          label="Table"
          value={table}
          onChange={(v) => updateDB({ table: v, query: '' })}
          options={[
            { value: '', label: 'Select a table...' },
            ...testStatus.tables.map((t) => ({ value: t, label: t })),
          ]}
        />
      ) : (
        <StyledInput
          label="Table"
          placeholder="users"
          value={table}
          onChange={(v) => updateDB({ table: v })}
        />
      )}

      <StyledInput
        label="Custom Query (optional, overrides table)"
        placeholder="SELECT * FROM users WHERE active = true"
        value={query}
        onChange={(v) => updateDB({ query: v })}
      />

      <StyledInput
        label="Poll Interval (seconds, 0 = one-shot)"
        placeholder="0"
        value={String(pollInterval)}
        onChange={(v) => updateDB({ poll_interval_seconds: parseInt(v) || 0 })}
      />

      {deployedConnectionId ? (
        <div className="p-3 bg-green-50 dark:bg-green-900/20 border border-green-200 dark:border-green-800 rounded-md">
          <p className="text-xs text-green-600 dark:text-green-300">
            Pipeline deployed. The database consumer is running and publishing rows to NATS.
          </p>
        </div>
      ) : (
        <p className="text-xs text-neutral-500 dark:text-neutral-400 mt-2">
          Configure the source database, then deploy the pipeline to start pulling data.
        </p>
      )}
    </div>
  )
}

function DatabaseProducerConfig({
  config,
  setConfig,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
}) {
  const [testStatus, setTestStatus] = useState<{ ok?: boolean; error?: string; tables?: string[] } | null>(null)
  const [testing, setTesting] = useState(false)

  const dbConfig = (config.database as Record<string, unknown>) || {}
  const host = (dbConfig.host as string) || ''
  const port = (dbConfig.port as number) || 5432
  const user = (dbConfig.user as string) || ''
  const password = (dbConfig.password as string) || ''
  const database = (dbConfig.database as string) || ''
  const table = (dbConfig.table as string) || ''
  const mode = (dbConfig.mode as string) || 'create_insert'

  const updateDB = (updates: Record<string, unknown>) => {
    setConfig({
      ...config,
      database: { ...dbConfig, ...updates },
    })
  }

  const testConnection = async () => {
    setTesting(true)
    setTestStatus(null)
    try {
      const resp = await fetch('http://localhost:9500/test-connection/', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ host, port, user, password, database }),
      })
      const data = await resp.json()
      setTestStatus(data)
    } catch (err) {
      setTestStatus({ ok: false, error: 'Cannot reach db-producer service. Is it running?' })
    }
    setTesting(false)
  }

  return (
    <div className="space-y-3">
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 100px', gap: '8px' }}>
        <StyledInput
          label="Host"
          placeholder="localhost or db.example.com"
          value={host}
          onChange={(v) => updateDB({ host: v })}
        />
        <StyledInput
          label="Port"
          placeholder="5432"
          value={String(port)}
          onChange={(v) => updateDB({ port: parseInt(v) || 5432 })}
        />
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
        <StyledInput
          label="Username"
          placeholder="postgres"
          value={user}
          onChange={(v) => updateDB({ user: v })}
        />
        <SecretInput
          label="Password"
          placeholder="••••••••"
          field="password"
          config={dbConfig}
          defaultSecretName={`pg-pwd-${host || 'db'}`}
          onChange={(patch) => updateDB(patch)}
        />
      </div>
      <StyledInput
        label="Database"
        placeholder="my_database"
        value={database}
        onChange={(v) => updateDB({ database: v })}
      />

      <button
        onClick={testConnection}
        disabled={testing || !host || !database || !user}
        style={{
          width: '100%',
          padding: '8px 12px',
          backgroundColor: testing ? '#9ca3af' : '#2563eb',
          color: '#fff',
          border: 'none',
          borderRadius: '6px',
          fontSize: '13px',
          fontWeight: 600,
          cursor: testing ? 'not-allowed' : 'pointer',
        }}
      >
        {testing ? 'Testing...' : 'Test Connection'}
      </button>

      {testStatus && (
        <div style={{
          padding: '8px 12px',
          borderRadius: '6px',
          fontSize: '12px',
          backgroundColor: testStatus.ok ? '#f0fdf4' : '#fef2f2',
          border: `1px solid ${testStatus.ok ? '#bbf7d0' : '#fecaca'}`,
          color: testStatus.ok ? '#166534' : '#991b1b',
        }}>
          {testStatus.ok ? (
            <>
              <strong>Connected!</strong>
              {testStatus.tables && testStatus.tables.length > 0 && (
                <div style={{ marginTop: '4px' }}>
                  Existing tables: {testStatus.tables.join(', ')}
                </div>
              )}
            </>
          ) : (
            <>{testStatus.error}</>
          )}
        </div>
      )}

      <StyledInput
        label="Target Table"
        placeholder="my_table"
        value={table}
        onChange={(v) => updateDB({ table: v })}
      />

      <StyledSelect
        label="Write Mode"
        value={mode}
        onChange={(v) => updateDB({ mode: v })}
        options={[
          { value: 'create_insert', label: 'Create table + Insert rows' },
          { value: 'insert', label: 'Insert into existing table' },
        ]}
      />

      <p className="text-xs text-neutral-500 dark:text-neutral-400 mt-2">
        Data flowing through the pipeline will be written to this database. For testing, use <code className="bg-neutral-100 dark:bg-neutral-800 px-1 rounded">postgres-target</code> (port 5433).
      </p>
    </div>
  )
}

const FILTER_OPERATORS: Array<{ value: string; label: string }> = [
  { value: 'equals', label: 'Equals' },
  { value: 'not_equals', label: 'Not Equals' },
  { value: 'contains', label: 'Contains' },
  { value: 'not_contains', label: 'Not Contains' },
  { value: 'starts_with', label: 'Starts With' },
  { value: 'ends_with', label: 'Ends With' },
  { value: 'gt', label: 'Greater Than' },
  { value: 'gte', label: 'Greater or Equal' },
  { value: 'lt', label: 'Less Than' },
  { value: 'lte', label: 'Less or Equal' },
  { value: 'is_empty', label: 'Is Empty' },
  { value: 'is_not_empty', label: 'Is Not Empty' },
]

function FilterConfig({
  config,
  setConfig,
  allNodes,
  allEdges,
  currentNodeId,
  deployedConnectionId,
}: {
  config: Record<string, unknown>
  setConfig: (c: Record<string, unknown>) => void
  allNodes?: Node[]
  allEdges?: Edge[]
  currentNodeId?: string
  deployedConnectionId?: string
}) {
  const rules = (config.rules as Array<{ field: string; operator: string; value: string }>) || []
  const logic = (config.logic as string) || 'and'
  const flattenPath = (config.flatten_path as string) || ''
  const flattenFields = (config.flatten_fields as Record<string, string>) || {}
  const flattenInclude = (config.flatten_include as Record<string, string>) || {}

  // Data structure browser state
  const [sampleData, setSampleData] = useState<unknown>(null)
  const [loadingData, setLoadingData] = useState(false)
  const [dataError, setDataError] = useState('')
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(new Set())
  const [searchTerm, setSearchTerm] = useState('')

  const updateRule = (index: number, field: string, value: string) => {
    const newRules = [...rules]
    newRules[index] = { ...newRules[index], [field]: value }
    setConfig({ ...config, rules: newRules })
  }

  const addRule = () => {
    setConfig({ ...config, rules: [...rules, { field: '', operator: 'equals', value: '' }] })
  }

  const removeRule = (index: number) => {
    setConfig({ ...config, rules: rules.filter((_, i) => i !== index) })
  }

  // Collect all paths up to a given depth for auto-expanding
  const collectPaths = (data: unknown, path: string, depth: number, maxDepth: number): string[] => {
    if (depth >= maxDepth || data === null || data === undefined) return []
    const paths: string[] = []
    if (Array.isArray(data)) {
      if (path) paths.push(path)
      if (data.length > 0) paths.push(...collectPaths(data[0], path, depth + 1, maxDepth))
    } else if (typeof data === 'object') {
      for (const [key, val] of Object.entries(data as Record<string, unknown>)) {
        const fullPath = path ? `${path}.${key}` : key
        if (val !== null && typeof val === 'object') {
          paths.push(fullPath)
          paths.push(...collectPaths(val, fullPath, depth + 1, maxDepth))
        }
      }
    }
    return paths
  }

  // Check if a key or any descendant matches the search
  const matchesSearch = (data: unknown, key: string): boolean => {
    if (!searchTerm) return true
    const term = searchTerm.toLowerCase()
    if (key.toLowerCase().includes(term)) return true
    if (data === null || data === undefined) return false
    if (Array.isArray(data)) return data.length > 0 && matchesSearch(data[0], '')
    if (typeof data === 'object') {
      return Object.entries(data as Record<string, unknown>).some(([k, v]) => matchesSearch(v, k))
    }
    return String(data).toLowerCase().includes(term)
  }

  // Fetch sample data from upstream consumer
  const fetchSampleData = async () => {
    // Walk upstream via edges to find the consumer that *actually* feeds this
    // filter, not just the first input in the pipeline.
    const consumer = (currentNodeId && allNodes && allEdges)
      ? findUpstreamConsumer(currentNodeId, allNodes, allEdges)
      : allNodes?.find(n => n.type === 'input')
    if (!consumer) { setDataError('No upstream consumer connected to this node'); return }

    const consumerConfig = consumer.data?.config as Record<string, unknown> | undefined
    const consumerType = consumerConfig?.type as string

    setLoadingData(true)
    setDataError('')

    try {
      if (consumerType === 'api') {
        const api = consumerConfig?.api as { base_url?: string; endpoints?: Array<{ path?: string; params?: string; auth_type?: string; auth_value?: string }> } | undefined
        const endpoint = api?.endpoints?.[0]
        if (!api?.base_url || !endpoint) { setDataError('Input has no API config'); return }

        const resp = await fetch('http://localhost:9800/sample-data/', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            base_url: api.base_url,
            path: endpoint.path || '/',
            params: endpoint.params || '',
            auth_type: endpoint.auth_type || 'none',
            auth_value: endpoint.auth_value || '',
          }),
        })
        const result = await resp.json()
        if (result.ok) {
          setSampleData(result.data)
          setExpandedPaths(new Set(collectPaths(result.data, '', 0, 3)))
        } else {
          setDataError(result.error || 'Failed to fetch')
        }
      } else if (consumerType === 'database') {
        const db = consumerConfig?.database as Record<string, unknown> | undefined
        if (!db?.host) { setDataError('Input has no database config'); return }
        const resp = await fetch('http://localhost:9300/sample-data/', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ ...db, limit: 5 }),
        })
        const result = await resp.json()
        if (result.ok) {
          setSampleData(result.rows)
          setExpandedPaths(new Set(collectPaths(result.rows, '', 0, 3)))
        } else {
          setDataError(result.error || 'Failed to fetch')
        }
      } else if (consumerType === 'tenant') {
        // Tenant consumers: fetch directly from the source tenant's last_payload
        // — no need to deploy our own pipeline first.
        const tenantCfg = consumerConfig?.tenant as { source_tenant_id?: string; source_connection_id?: string } | undefined
        const srcTenant = tenantCfg?.source_tenant_id
        const srcConn = tenantCfg?.source_connection_id
        if (!srcTenant) { setDataError('Tenant consumer has no source configured'); return }
        const { default: apiClient } = await import('../../services/api')
        const params = new URLSearchParams({ source_tenant_id: srcTenant })
        if (srcConn) params.set('source_connection_id', srcConn)
        const resp = await apiClient.get(`/api/v1/sample-data/source?${params.toString()}`)
        const result = resp.data
        if (result.ok) {
          setSampleData(result.data)
          setExpandedPaths(new Set(collectPaths(result.data, '', 0, 3)))
        } else {
          setDataError(result.error || 'No sample available from source tenant yet')
        }
      } else if (consumerType === 'file') {
        // File consumers can preview pre-deploy by reading the most recently
        // modified file in the watch directory directly. Falls back to the
        // deployed-pipeline endpoint if the file-consumer service is
        // unreachable (e.g. running outside Docker).
        const fileCfg = consumerConfig?.file as { path?: string } | undefined
        const watchPath = fileCfg?.path
        if (watchPath) {
          try {
            const resp = await fetch('http://localhost:9200/sample-data/', {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ path: watchPath }),
            })
            const result = await resp.json()
            if (result.ok) {
              setSampleData(result.data)
              setExpandedPaths(new Set(collectPaths(result.data, '', 0, 3)))
              return
            }
            // Surface "no files yet" but keep the deploy fallback below as
            // a second chance — if the pipeline ran in the past we may
            // still have a cached last_payload.
            setDataError(result.error || 'No files in the watch directory yet')
          } catch {
            // Fall through to deploy-based path.
          }
        }
        const connId = deployedConnectionId
        if (!connId) { setDataError('Set a watch directory with at least one file, or deploy the pipeline first'); return }
        const { default: apiClient } = await import('../../services/api')
        const resp = await apiClient.get(`/api/v1/connections/${connId}/sample-data`)
        const result = resp.data
        if (result.ok) {
          setSampleData(result.data)
          setExpandedPaths(new Set(collectPaths(result.data, '', 0, 3)))
        } else {
          setDataError(result.error || 'No data yet — send data through the pipeline first')
        }
      } else if (consumerType === 'http') {
        // HTTP webhooks fundamentally need a deployed URL.
        const connId = deployedConnectionId
        if (!connId) { setDataError('Deploy the pipeline and send a webhook first'); return }
        const { default: apiClient } = await import('../../services/api')
        const resp = await apiClient.get(`/api/v1/connections/${connId}/sample-data`)
        const result = resp.data
        if (result.ok) {
          setSampleData(result.data)
          setExpandedPaths(new Set(collectPaths(result.data, '', 0, 3)))
        } else {
          setDataError(result.error || 'No data yet — send data through the pipeline first')
        }
      } else {
        setDataError(`Preview not available for "${consumerType || 'unknown'}" input type`)
      }
    } catch (err) {
      setDataError('Connection failed — is the service running?')
    } finally {
      setLoadingData(false)
    }
  }

  // Recursive tree renderer
  const renderTree = (data: unknown, path: string, depth: number, insideListPath: boolean, parentMatched: boolean = false): JSX.Element | null => {
    if (data === null || data === undefined) return null
    const indent = depth * 12
    const isSearching = searchTerm.length > 0

    if (Array.isArray(data)) {
      const isExpanded = isSearching || expandedPaths.has(path)
      const isSelectedAsList = flattenPath === path
      const label = path.split('.').pop() || 'root'
      return (
        <div>
          <div
            style={{
              display: 'flex', alignItems: 'center', gap: '4px',
              padding: '4px 4px 4px ' + indent + 'px', cursor: 'pointer',
              borderRadius: '4px', backgroundColor: isSelectedAsList ? '#eff6ff' : 'transparent',
              overflow: 'hidden',
            }}
            onClick={() => {
              const next = new Set(expandedPaths)
              isExpanded ? next.delete(path) : next.add(path)
              setExpandedPaths(next)
            }}
          >
            <span style={{ fontSize: '10px', color: '#6b7280', width: '12px', textAlign: 'center', flexShrink: 0 }}>{isExpanded ? '▾' : '▸'}</span>
            <span style={{ fontSize: '11px', color: '#1e40af', fontFamily: 'monospace', fontWeight: 600, whiteSpace: 'nowrap' }}>
              {label}
            </span>
            <span style={{ fontSize: '9px', color: '#94a3b8', flexShrink: 0 }}>
              {data.length}
            </span>
            {!isSelectedAsList ? (
              <button
                onClick={(e) => { e.stopPropagation(); setConfig({ ...config, flatten_path: path }) }}
                style={{ fontSize: '9px', padding: '2px 6px', background: '#dbeafe', color: '#1d4ed8', border: '1px solid #93c5fd', borderRadius: '3px', cursor: 'pointer', marginLeft: 'auto', fontWeight: 600, whiteSpace: 'nowrap', flexShrink: 0 }}
              >Split into rows</button>
            ) : (
              <span style={{ fontSize: '9px', padding: '2px 6px', background: '#dcfce7', color: '#16a34a', borderRadius: '3px', marginLeft: 'auto', fontWeight: 600, whiteSpace: 'nowrap', flexShrink: 0 }}>
                Splitting
              </span>
            )}
          </div>
          {isExpanded && data.length > 0 && (
            <div style={{ borderLeft: '1px solid #e2e8f0', marginLeft: indent + 8 }}>
              {renderTree(data[0], path, depth + 1, isSelectedAsList || insideListPath, parentMatched)}
            </div>
          )}
        </div>
      )
    }

    if (typeof data === 'object') {
      return (
        <div>
          {Object.entries(data as Record<string, unknown>).map(([key, val]) => {
            const fullPath = path ? `${path}.${key}` : key

            // Search filtering — match key, full path, or parent path prefix
            const term = searchTerm.toLowerCase()
            const fp = fullPath.toLowerCase()
            // "issue.title" matches fullPath "issue.title"; "issue" matches key "issue"
            // "issue." keeps parent visible so children can render
            const pathMatches = fp.includes(term) || term.includes(fp)
            const keyMatches = isSearching && (key.toLowerCase().includes(term) || pathMatches)
            if (isSearching && !parentMatched && !keyMatches && !matchesSearch(val, key)) return null

            const isExpanded = expandedPaths.has(fullPath) || (isSearching && !expandedPaths.has('_collapsed_' + fullPath))

            if (val !== null && typeof val === 'object') {
              return (
                <div key={key}>
                  <div
                    style={{
                      display: 'flex', alignItems: 'center', gap: '4px',
                      padding: '3px 4px 3px ' + indent + 'px', cursor: 'pointer',
                      borderRadius: '4px',
                    }}
                    onClick={() => {
                      const next = new Set(expandedPaths)
                      if (isExpanded) {
                        next.delete(fullPath)
                        if (isSearching) next.add('_collapsed_' + fullPath)
                      } else {
                        next.add(fullPath)
                        next.delete('_collapsed_' + fullPath)
                      }
                      setExpandedPaths(next)
                    }}
                  >
                    <span style={{ fontSize: '10px', color: '#6b7280', width: '12px', textAlign: 'center', flexShrink: 0 }}>{isExpanded ? '▾' : '▸'}</span>
                    <span style={{ fontSize: '11px', color: '#374151', fontFamily: 'monospace', fontWeight: 500 }}>{key}</span>
                  </div>
                  {isExpanded && (
                    <div style={{ borderLeft: '1px solid #e2e8f0', marginLeft: indent + 8 }}>
                      {renderTree(val, fullPath, depth + 1, insideListPath, (keyMatches && !term.includes('.')) || parentMatched)}
                    </div>
                  )}
                </div>
              )
            }

            // Leaf value
            const displayVal = String(val)
            const shortVal = displayVal.length > 25 ? displayVal.slice(0, 25) + '...' : displayVal
            const leafName = key
            let fieldPath = fullPath
            if (flattenPath && insideListPath && fullPath.startsWith(flattenPath + '.')) {
              fieldPath = fullPath.slice(flattenPath.length + 1)
            }
            const extractFields = (config.extract_fields as string[]) || []
            const isAlreadyAdded = Object.keys(flattenFields).includes(fieldPath) || Object.keys(flattenInclude).includes(fullPath) || extractFields.includes(fullPath)
            const highlightMatch = isSearching && key.toLowerCase().includes(searchTerm.toLowerCase())

            const addField = () => {
              if (isAlreadyAdded) return
              if (flattenPath && insideListPath) {
                setConfig({ ...config, flatten_fields: { ...flattenFields, [fieldPath]: leafName } })
              } else if (flattenPath) {
                setConfig({ ...config, flatten_include: { ...flattenInclude, [fullPath]: leafName } })
              } else {
                setConfig({ ...config, extract_fields: [...extractFields, fullPath] })
              }
            }

            return (
              <div key={key} style={{
                display: 'flex', alignItems: 'center', gap: '4px',
                padding: '2px 4px 2px ' + (indent + 12) + 'px',
                borderRadius: '4px', overflow: 'hidden',
                backgroundColor: highlightMatch ? '#fefce8' : (isAlreadyAdded ? '#f0fdf4' : 'transparent'),
                cursor: isAlreadyAdded ? 'default' : 'pointer',
              }}
              onClick={addField}
              >
                <span style={{ fontSize: '11px', fontFamily: 'monospace', color: isAlreadyAdded ? '#16a34a' : '#374151', fontWeight: highlightMatch || isAlreadyAdded ? 600 : 400, whiteSpace: 'nowrap' }}>{key}</span>
                <span style={{ fontSize: '10px', color: '#94a3b8', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0 }}>{shortVal}</span>
                {isAlreadyAdded ? (
                  <span style={{ fontSize: '10px', color: '#16a34a', fontWeight: 600, flexShrink: 0 }}>✓</span>
                ) : (
                  <span style={{ fontSize: '9px', padding: '1px 6px', background: '#f0fdf4', color: '#16a34a', border: '1px solid #bbf7d0', borderRadius: '3px', fontWeight: 600, flexShrink: 0 }}>+</span>
                )}
              </div>
            )
          })}
        </div>
      )
    }

    return null
  }

  // Count total picked fields
  const extractFields = (config.extract_fields as string[]) || []
  const pickedCount = Object.keys(flattenFields).length + Object.keys(flattenInclude).length + extractFields.length

  return (
    <div>
      {/* ---- Pick Fields Section ---- */}
      <div style={{ marginBottom: '16px' }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '8px' }}>
          <span style={{ fontSize: '13px', fontWeight: 700, color: '#374151' }}>Pick Fields</span>
          <span style={{ fontSize: '11px', color: '#9ca3af' }}>Choose which data to keep</span>
        </div>

        {/* Show Data Structure button */}
        <button
          onClick={fetchSampleData}
          disabled={loadingData}
          style={{
            padding: '8px 14px', fontSize: '12px', fontWeight: 600, width: '100%',
            backgroundColor: loadingData ? '#e5e7eb' : '#eff6ff', color: loadingData ? '#9ca3af' : '#1d4ed8',
            border: '1px solid ' + (loadingData ? '#d1d5db' : '#93c5fd'),
            borderRadius: '6px', cursor: loadingData ? 'default' : 'pointer', marginBottom: '8px',
          }}
        >
          {loadingData ? 'Loading...' : sampleData ? 'Refresh Data Structure' : 'Show Data Structure'}
        </button>
        {dataError ? <p style={{ fontSize: '11px', color: '#dc2626', margin: '4px 0' }}>{dataError}</p> : null}

        {/* Data structure tree */}
        {sampleData ? (
          <div style={{
            border: '1px solid #e5e7eb', borderRadius: '6px', backgroundColor: '#fafafa', marginBottom: '8px', overflow: 'hidden',
          }}>
            <div style={{ padding: '8px 8px 4px', borderBottom: '1px solid #e5e7eb', background: '#f8fafc' }}>
              <input
                placeholder="Search fields... (e.g. temperature)"
                value={searchTerm}
                onChange={(e) => setSearchTerm(e.target.value)}
                style={{
                  width: '100%', padding: '6px 10px', fontSize: '12px', border: '1px solid #d1d5db',
                  borderRadius: '4px', boxSizing: 'border-box', outline: 'none',
                }}
              />
              <p style={{ fontSize: '10px', color: '#94a3b8', margin: '4px 0 2px' }}>
                Click fields to add them. Click a list to "Split into rows".
              </p>
            </div>
            <div style={{ maxHeight: '350px', overflowY: 'auto', overflowX: 'hidden', padding: '4px 0' }}>
              {renderTree(sampleData, '', 0, false)}
            </div>
          </div>
        ) : null}

        {/* Split into rows indicator */}
        {flattenPath && (
          <div style={{
            display: 'flex', alignItems: 'center', gap: '6px', padding: '6px 8px', marginBottom: '8px',
            backgroundColor: '#eff6ff', border: '1px solid #93c5fd', borderRadius: '6px',
          }}>
            <span style={{ fontSize: '11px', color: '#1d4ed8' }}>
              Splitting <code style={{ background: '#dbeafe', padding: '1px 4px', borderRadius: '2px' }}>{flattenPath}</code> into rows
            </span>
            <button
              onClick={() => setConfig({ ...config, flatten_path: '', flatten_fields: {}, flatten_include: {} })}
              style={{ fontSize: '11px', padding: '1px 6px', background: 'none', border: '1px solid #93c5fd', borderRadius: '3px', color: '#1d4ed8', cursor: 'pointer', marginLeft: 'auto' }}
            >Clear</button>
          </div>
        )}

        {/* Selected fields summary */}
        {pickedCount > 0 && (
          <div style={{ marginBottom: '8px' }}>
            <p style={{ fontSize: '11px', fontWeight: 600, color: '#374151', marginBottom: '4px' }}>
              Selected fields ({pickedCount})
            </p>
            {extractFields.map((path) => (
              <div key={'ef-' + path} style={{
                display: 'flex', alignItems: 'center', gap: '4px', padding: '4px 8px', marginBottom: '3px',
                backgroundColor: '#f0fdf4', border: '1px solid #bbf7d0', borderRadius: '4px', fontSize: '11px',
              }}>
                <code style={{ fontFamily: 'monospace', color: '#374151', flex: 1 }}>{path}</code>
                <button
                  onClick={() => {
                    setConfig({ ...config, extract_fields: extractFields.filter(f => f !== path) })
                  }}
                  style={{ background: 'none', border: 'none', color: '#dc2626', cursor: 'pointer', fontSize: '13px', padding: '0 4px' }}
                >×</button>
              </div>
            ))}
            {Object.entries(flattenFields).map(([path, name]) => (
              <div key={'ff-' + path} style={{
                display: 'flex', alignItems: 'center', gap: '4px', padding: '4px 8px', marginBottom: '3px',
                backgroundColor: '#f0fdf4', border: '1px solid #bbf7d0', borderRadius: '4px', fontSize: '11px',
              }}>
                <code style={{ fontFamily: 'monospace', color: '#374151', flex: 1 }}>{path}</code>
                <span style={{ color: '#9ca3af' }}>→</span>
                <input
                  value={name}
                  onChange={(e) => setConfig({ ...config, flatten_fields: { ...flattenFields, [path]: e.target.value } })}
                  style={{ width: '80px', padding: '2px 4px', fontSize: '11px', border: '1px solid #d1d5db', borderRadius: '3px', fontFamily: 'monospace' }}
                />
                <button
                  onClick={() => {
                    const nf = { ...flattenFields }; delete nf[path]
                    setConfig({ ...config, flatten_fields: nf })
                  }}
                  style={{ background: 'none', border: 'none', color: '#dc2626', cursor: 'pointer', fontSize: '13px', padding: '0 4px' }}
                >×</button>
              </div>
            ))}
            {Object.entries(flattenInclude).map(([path, name]) => (
              <div key={'fi-' + path} style={{
                display: 'flex', alignItems: 'center', gap: '4px', padding: '4px 8px', marginBottom: '3px',
                backgroundColor: '#fefce8', border: '1px solid #fde68a', borderRadius: '4px', fontSize: '11px',
              }}>
                <code style={{ fontFamily: 'monospace', color: '#374151', flex: 1 }}>{path}</code>
                <span style={{ color: '#9ca3af' }}>→</span>
                <input
                  value={name}
                  onChange={(e) => setConfig({ ...config, flatten_include: { ...flattenInclude, [path]: e.target.value } })}
                  style={{ width: '80px', padding: '2px 4px', fontSize: '11px', border: '1px solid #d1d5db', borderRadius: '3px', fontFamily: 'monospace' }}
                />
                <button
                  onClick={() => {
                    const ni = { ...flattenInclude }; delete ni[path]
                    setConfig({ ...config, flatten_include: ni })
                  }}
                  style={{ background: 'none', border: 'none', color: '#dc2626', cursor: 'pointer', fontSize: '13px', padding: '0 4px' }}
                >×</button>
              </div>
            ))}
          </div>
        )}

        {!sampleData && pickedCount === 0 && (
          <p style={{ fontSize: '12px', color: '#9ca3af', fontStyle: 'italic' }}>
            Click "Show Data Structure" to browse and pick the fields you want to keep.
          </p>
        )}
      </div>

      {/* ---- Filter Rules Section ---- */}
      <div style={{ borderTop: '1px solid #e5e7eb', paddingTop: '12px' }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '8px' }}>
          <span style={{ fontSize: '13px', fontWeight: 700, color: '#374151' }}>Filter Rules</span>
          <span style={{ fontSize: '11px', color: '#9ca3af' }}>Optional — drop rows that don't match</span>
        </div>

        {rules.length > 0 && (
          <div style={{ marginBottom: '8px', display: 'flex', alignItems: 'center', gap: '8px' }}>
            <span style={{ fontSize: '11px', fontWeight: 600, color: '#6b7280' }}>Match:</span>
            {['and', 'or'].map((l) => (
              <button
                key={l}
                onClick={() => setConfig({ ...config, logic: l })}
                style={{
                  padding: '3px 10px', fontSize: '11px', fontWeight: 600,
                  backgroundColor: logic === l ? (l === 'and' ? '#dbeafe' : '#fef3c7') : '#f3f4f6',
                  color: logic === l ? (l === 'and' ? '#1d4ed8' : '#92400e') : '#6b7280',
                  border: `1px solid ${logic === l ? (l === 'and' ? '#93c5fd' : '#fcd34d') : '#d1d5db'}`,
                  borderRadius: '4px', cursor: 'pointer',
                }}
              >
                {l.toUpperCase()}
              </button>
            ))}
            <span style={{ fontSize: '10px', color: '#9ca3af' }}>
              {logic === 'and' ? 'All must match' : 'Any can match'}
            </span>
          </div>
        )}

        {rules.map((rule, i) => (
          <div key={i} style={{
            display: 'flex', gap: '4px', alignItems: 'center', marginBottom: '6px',
            padding: '6px', backgroundColor: '#f9fafb', borderRadius: '6px', border: '1px solid #e5e7eb',
          }}>
            <input
              placeholder="field"
              value={rule.field}
              onChange={(e) => updateRule(i, 'field', e.target.value)}
              style={{ flex: 1, padding: '5px 6px', fontSize: '11px', border: '1px solid #d1d5db', borderRadius: '4px', fontFamily: 'monospace' }}
            />
            <select
              value={rule.operator}
              onChange={(e) => updateRule(i, 'operator', e.target.value)}
              style={{ padding: '5px 4px', fontSize: '11px', border: '1px solid #d1d5db', borderRadius: '4px', backgroundColor: 'white' }}
            >
              {FILTER_OPERATORS.map((op) => (
                <option key={op.value} value={op.value}>{op.label}</option>
              ))}
            </select>
            {!['is_empty', 'is_not_empty'].includes(rule.operator) && (
              <input
                placeholder="value"
                value={rule.value}
                onChange={(e) => updateRule(i, 'value', e.target.value)}
                style={{ flex: 1, padding: '5px 6px', fontSize: '11px', border: '1px solid #d1d5db', borderRadius: '4px' }}
              />
            )}
            <button
              onClick={() => removeRule(i)}
              style={{ padding: '2px 6px', fontSize: '14px', background: 'none', border: 'none', color: '#dc2626', cursor: 'pointer' }}
            >×</button>
          </div>
        ))}

        <button
          onClick={addRule}
          style={{
            padding: '5px 10px', fontSize: '11px', fontWeight: 600,
            backgroundColor: '#f3f4f6', border: '1px solid #d1d5db',
            borderRadius: '4px', cursor: 'pointer', color: '#374151',
          }}
        >
          + Add Rule
        </button>
      </div>
    </div>
  )
}

function ConverterConfig({
  config,
  setConfig,
  allNodes,
  allEdges,
  currentNodeId,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
  allNodes?: Node[]
  allEdges?: Edge[]
  currentNodeId?: string
}) {
  const outputFormat = (config.output_format as string) || ''
  const csvDelimiter = (config.csv_delimiter as string) || ','
  const csvHeaders = (config.csv_headers as boolean) !== false
  const textTemplate = (config.text_template as string) || ''
  const xmlRootTag = (config.xml_root_tag as string) || 'records'
  const xmlRowTag = (config.xml_row_tag as string) || 'record'
  const mappings = (config.mappings as Array<{ source: string; target: string; type: string; value?: unknown; expression?: string }>) || []
  const dropUnmapped = (config.drop_unmapped as boolean) || false
  const [previewInput, setPreviewInput] = useState('')
  const [previewOutput, setPreviewOutput] = useState('')
  const [previewing, setPreviewing] = useState(false)
  const [fetchingSample, setFetchingSample] = useState(false)

  // Get the consumer that actually feeds this converter (walk edges upstream)
  const consumerNode = (currentNodeId && allNodes && allEdges)
    ? findUpstreamConsumer(currentNodeId, allNodes, allEdges)
    : allNodes?.find(n => n.type === 'input')
  const consumerConfig = consumerNode?.data?.config as Record<string, unknown> | undefined
  const consumerType = consumerConfig?.type as string | undefined

  const fetchSampleData = async () => {
    if (!consumerConfig) return
    setFetchingSample(true)
    try {
      if (consumerType === 'database') {
        const dc = (consumerConfig.database as Record<string, unknown>) || {}
        const resp = await fetch('http://localhost:9300/sample-data/', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            host: dc.host, port: dc.port || 5432, user: dc.user,
            password: dc.password, database: dc.database,
            table: dc.table, query: dc.query, limit: 3,
          }),
        })
        const data = await resp.json()
        if (data.ok && data.rows) {
          setPreviewInput(JSON.stringify(data.rows, null, 2))
        } else {
          setPreviewInput('// Error: ' + (data.error || 'No data'))
        }
      } else if (consumerType === 'file') {
        // Pre-deploy file preview reads the most recently modified file in
        // the watch directory directly. Same endpoint the filter uses.
        const fc = (consumerConfig.file as Record<string, unknown>) || {}
        const watchPath = fc.path as string | undefined
        if (!watchPath) {
          setPreviewInput('// Set a watch directory on the file consumer first')
          return
        }
        const resp = await fetch('http://localhost:9200/sample-data/', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ path: watchPath }),
        })
        const data = await resp.json()
        if (data.ok) {
          setPreviewInput(JSON.stringify(data.data, null, 2))
        } else {
          setPreviewInput('// Error: ' + (data.error || 'No files in watch directory'))
        }
      }
    } catch (err) {
      setPreviewInput('// Error fetching sample: ' + (err instanceof Error ? err.message : 'unknown'))
    }
    setFetchingSample(false)
  }

  const updateMapping = (index: number, updates: Record<string, unknown>) => {
    const newMappings = [...mappings]
    newMappings[index] = { ...newMappings[index], ...updates } as typeof mappings[number]
    setConfig({ ...config, mappings: newMappings })
  }

  const addMapping = () => {
    setConfig({
      ...config,
      mappings: [...mappings, { source: '', target: '', type: 'rename' }],
    })
  }

  const removeMapping = (index: number) => {
    setConfig({
      ...config,
      mappings: mappings.filter((_, i) => i !== index),
    })
  }

  const runPreview = async () => {
    setPreviewing(true)
    try {
      const resp = await fetch('http://localhost:9600/preview/', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          mappings, drop_unmapped: dropUnmapped,
          output_format: outputFormat,
          csv_delimiter: csvDelimiter, csv_headers: csvHeaders,
          text_template: textTemplate,
          xml_root_tag: xmlRootTag, xml_row_tag: xmlRowTag,
          sample_data: previewInput,
        }),
      })
      const data = await resp.json()
      setPreviewOutput(data.result_text || JSON.stringify(data.result, null, 2))
    } catch (err) {
      setPreviewOutput('Error: ' + (err instanceof Error ? err.message : 'Preview failed'))
    }
    setPreviewing(false)
  }

  return (
    <div className="space-y-3">
      {/* Output Format */}
      <StyledSelect
        label="Output Format"
        value={outputFormat}
        onChange={(v) => setConfig({ ...config, output_format: v })}
        options={[
          { value: '', label: 'JSON (no conversion)' },
          { value: 'csv', label: 'CSV' },
          { value: 'tsv', label: 'TSV (Tab-separated)' },
          { value: 'xml', label: 'XML' },
          { value: 'text', label: 'Plain Text (custom template)' },
          { value: 'yaml', label: 'YAML' },
          { value: 'ndjson', label: 'NDJSON (line-delimited JSON)' },
        ]}
      />

      {outputFormat === 'csv' && (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr auto', gap: '8px', alignItems: 'end' }}>
          <StyledInput
            label="Delimiter"
            placeholder=","
            value={csvDelimiter}
            onChange={(v) => setConfig({ ...config, csv_delimiter: v })}
          />
          <label style={{ display: 'flex', alignItems: 'center', gap: '4px', fontSize: '11px', color: '#374151', paddingBottom: '8px' }}>
            <input type="checkbox" checked={csvHeaders} onChange={(e) => setConfig({ ...config, csv_headers: e.target.checked })} />
            Headers
          </label>
        </div>
      )}

      {outputFormat === 'xml' && (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
          <StyledInput label="Root Tag" placeholder="records" value={xmlRootTag} onChange={(v) => setConfig({ ...config, xml_root_tag: v })} />
          <StyledInput label="Row Tag" placeholder="record" value={xmlRowTag} onChange={(v) => setConfig({ ...config, xml_row_tag: v })} />
        </div>
      )}

      {outputFormat === 'text' && (
        <>
          <StyledInput
            label="Text Template (per row)"
            placeholder="{name} <{email}>"
            value={textTemplate}
            onChange={(v) => setConfig({ ...config, text_template: v })}
          />
          <p className="text-xs text-neutral-500">Use <code className="bg-neutral-100 px-1 rounded">{'{field_name}'}</code> to insert field values. One line per row.</p>
        </>
      )}

      {/* Field Mappings - optional, applied before format conversion */}
      <div style={{ borderTop: '1px solid #e5e7eb', paddingTop: '8px', marginTop: '8px' }}>
        <div style={{ fontSize: '12px', fontWeight: 600, color: '#374151', marginBottom: '4px' }}>
          Field Mappings <span style={{ fontWeight: 400, color: '#9ca3af' }}>(optional, applied before format conversion)</span>
        </div>

        {mappings.map((m, i) => (
          <div key={i} style={{ backgroundColor: '#f9fafb', border: '1px solid #e5e7eb', borderRadius: '6px', padding: '8px', marginBottom: '6px' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: '4px', marginBottom: '6px' }}>
              <select
                value={m.type || 'rename'}
                onChange={(e) => updateMapping(i, { type: e.target.value })}
                style={{ fontSize: '11px', padding: '3px 6px', border: '1px solid #d1d5db', borderRadius: '4px', backgroundColor: '#fff' }}
              >
                <option value="rename">Rename</option>
                <option value="copy">Copy</option>
                <option value="remove">Remove</option>
                <option value="static">Static Value</option>
                <option value="template">Template</option>
                <option value="to_string">To String</option>
                <option value="to_number">To Number</option>
              </select>
              <div style={{ flex: 1 }} />
              <button onClick={() => removeMapping(i)} style={{ background: 'none', border: 'none', color: '#dc2626', cursor: 'pointer', fontSize: '14px', padding: '0 4px' }}>×</button>
            </div>
            {m.type !== 'static' && m.type !== 'template' && (
              <StyledInput label="Source Field" placeholder="e.g. name or user.email" value={m.source} onChange={(v) => updateMapping(i, { source: v })} />
            )}
            {m.type !== 'remove' && (
              <StyledInput label="Target Field" placeholder="e.g. full_name" value={m.target} onChange={(v) => updateMapping(i, { target: v })} />
            )}
            {m.type === 'static' && (
              <StyledInput label="Value" placeholder="e.g. default_value" value={String(m.value || '')} onChange={(v) => updateMapping(i, { value: v })} />
            )}
            {m.type === 'template' && (
              <StyledInput label="Template" placeholder="e.g. {first_name} {last_name}" value={m.expression || ''} onChange={(v) => updateMapping(i, { expression: v })} />
            )}
          </div>
        ))}

        <button
          onClick={addMapping}
          style={{ width: '100%', padding: '6px 12px', fontSize: '12px', fontWeight: 600, backgroundColor: '#f3f4f6', color: '#374151', border: '1px dashed #d1d5db', borderRadius: '6px', cursor: 'pointer' }}
        >
          + Add Mapping
        </button>

        <label style={{ display: 'flex', alignItems: 'center', gap: '6px', fontSize: '12px', color: '#374151', marginTop: '8px' }}>
          <input type="checkbox" checked={dropUnmapped} onChange={(e) => setConfig({ ...config, drop_unmapped: e.target.checked })} />
          Drop unmapped fields
        </label>
      </div>

      {/* Preview */}
      <div style={{ borderTop: '1px solid #e5e7eb', paddingTop: '8px', marginTop: '8px' }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '4px' }}>
          <div style={{ fontSize: '12px', fontWeight: 600, color: '#374151' }}>Preview</div>
          {consumerType === 'database' && (
            <button
              onClick={fetchSampleData}
              disabled={fetchingSample}
              style={{ fontSize: '11px', padding: '2px 8px', backgroundColor: '#f3f4f6', border: '1px solid #d1d5db', borderRadius: '4px', cursor: fetchingSample ? 'not-allowed' : 'pointer', color: '#374151' }}
            >
              {fetchingSample ? 'Fetching...' : 'Fetch from Input'}
            </button>
          )}
        </div>
        <textarea
          value={previewInput}
          onChange={(e) => setPreviewInput(e.target.value)}
          style={{ width: '100%', height: '80px', padding: '6px 8px', fontSize: '11px', fontFamily: 'monospace', border: '1px solid #d1d5db', borderRadius: '4px', resize: 'vertical', boxSizing: 'border-box' }}
          placeholder={consumerType === 'database' ? 'Click "Fetch from Input" to load real data, or paste JSON here' : '[{"field": "value"}]'}
        />
        <button
          onClick={runPreview}
          disabled={previewing || !previewInput.trim() || (!outputFormat && mappings.length === 0)}
          style={{ width: '100%', padding: '6px 12px', fontSize: '12px', fontWeight: 600, backgroundColor: previewing ? '#9ca3af' : '#7c3aed', color: '#fff', border: 'none', borderRadius: '6px', cursor: previewing ? 'not-allowed' : 'pointer', marginTop: '4px' }}
        >
          {previewing ? 'Running...' : 'Test Transform'}
        </button>
        {previewOutput && (
          <pre style={{ marginTop: '4px', padding: '6px 8px', fontSize: '11px', fontFamily: 'monospace', backgroundColor: '#f0fdf4', border: '1px solid #bbf7d0', borderRadius: '4px', maxHeight: '120px', overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{previewOutput}</pre>
        )}
      </div>
    </div>
  )
}

function ApiConsumerConfig({
  config,
  setConfig,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
}) {
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [advHovered, setAdvHovered] = useState(false)

  // Get current API config or initialize defaults
  const apiConfig = (config.api as Record<string, unknown>) || {}
  const baseUrl = (apiConfig.base_url as string) || ''
  const pollInterval = (apiConfig.poll_interval_seconds as number) || 60
  const oneTimeOnly = (apiConfig.one_time_only as boolean) || false
  const endpoints = (apiConfig.endpoints as ApiEndpoint[]) || []

  const updateApiConfig = (updates: Record<string, unknown>) => {
    setConfig({
      ...config,
      api: { ...apiConfig, ...updates },
    })
  }

  const addEndpoint = () => {
    const newEndpoints = [...endpoints, { path: '/', params: '', auth_type: 'none' as const, auth_value: '' }]
    updateApiConfig({ endpoints: newEndpoints })
  }

  const updateEndpoint = (index: number, field: keyof ApiEndpoint, value: string) => {
    const newEndpoints = [...endpoints]
    newEndpoints[index] = { ...newEndpoints[index], [field]: value }
    updateApiConfig({ endpoints: newEndpoints })
  }

  // patchEndpoint accepts a partial object — needed by SecretInput which sets
  // both the plaintext field (to undefined) and the new <field>_secret_id key.
  const patchEndpoint = (index: number, patch: Record<string, unknown>) => {
    const newEndpoints = [...endpoints]
    const merged: Record<string, unknown> = { ...(newEndpoints[index] as unknown as Record<string, unknown>) }
    for (const [k, v] of Object.entries(patch)) {
      if (v === undefined) delete merged[k]
      else merged[k] = v
    }
    newEndpoints[index] = merged as unknown as ApiEndpoint
    updateApiConfig({ endpoints: newEndpoints })
  }

  const removeEndpoint = (index: number) => {
    const newEndpoints = endpoints.filter((_, i) => i !== index)
    updateApiConfig({ endpoints: newEndpoints })
  }

  return (
    <div>
      {/* Base URL - always visible, minimal */}
      <StyledInput
        label="API Base URL"
        placeholder="https://api.met.no"
        value={baseUrl}
        onChange={(value) => updateApiConfig({ base_url: value })}
      />

      {/* Advanced toggle */}
      <button
        onClick={() => setShowAdvanced(!showAdvanced)}
        onMouseEnter={() => setAdvHovered(true)}
        onMouseLeave={() => setAdvHovered(false)}
        style={{
          width: '100%',
          padding: '8px 12px',
          marginBottom: '16px',
          backgroundColor: advHovered ? '#f3f4f6' : '#f9fafb',
          border: '1px solid #e5e7eb',
          borderRadius: '6px',
          fontSize: '12px',
          color: '#6b7280',
          cursor: 'pointer',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          transition: 'all 150ms ease',
        }}
      >
        <span>{showAdvanced ? 'Hide' : 'Show'} Advanced Options</span>
        <span style={{ transform: showAdvanced ? 'rotate(180deg)' : 'rotate(0deg)', transition: 'transform 150ms' }}>
          ▼
        </span>
      </button>

      {/* Advanced options - collapsible */}
      {showAdvanced && (
        <div
          style={{
            padding: '16px',
            backgroundColor: '#f9fafb',
            borderRadius: '6px',
            border: '1px solid #e5e7eb',
            marginBottom: '16px',
          }}
        >
          {/* Retrieval Mode Toggle */}
          <div style={{ marginBottom: '16px' }}>
            <label
              style={{
                display: 'block',
                fontSize: '12px',
                fontWeight: 600,
                color: '#374151',
                marginBottom: '8px',
                textTransform: 'uppercase',
                letterSpacing: '0.025em',
              }}
            >
              Retrieval Mode
            </label>
            <div style={{ display: 'flex', gap: '8px' }}>
              <button
                onClick={() => updateApiConfig({ one_time_only: false })}
                style={{
                  flex: 1,
                  padding: '10px 12px',
                  backgroundColor: !oneTimeOnly ? '#dbeafe' : '#ffffff',
                  border: !oneTimeOnly ? '2px solid #3b82f6' : '1px solid #d1d5db',
                  borderRadius: '6px',
                  fontSize: '13px',
                  fontWeight: !oneTimeOnly ? 600 : 400,
                  color: !oneTimeOnly ? '#1d4ed8' : '#6b7280',
                  cursor: 'pointer',
                  transition: 'all 150ms ease',
                }}
              >
                Poll Continuously
              </button>
              <button
                onClick={() => updateApiConfig({ one_time_only: true })}
                style={{
                  flex: 1,
                  padding: '10px 12px',
                  backgroundColor: oneTimeOnly ? '#dbeafe' : '#ffffff',
                  border: oneTimeOnly ? '2px solid #3b82f6' : '1px solid #d1d5db',
                  borderRadius: '6px',
                  fontSize: '13px',
                  fontWeight: oneTimeOnly ? 600 : 400,
                  color: oneTimeOnly ? '#1d4ed8' : '#6b7280',
                  cursor: 'pointer',
                  transition: 'all 150ms ease',
                }}
              >
                Retrieve Once
              </button>
            </div>
            <p style={{ fontSize: '11px', color: '#9ca3af', marginTop: '6px', fontStyle: 'italic' }}>
              {oneTimeOnly 
                ? 'Data will be fetched once when the pipeline starts'
                : 'Data will be fetched repeatedly at the specified interval'
              }
            </p>
          </div>

          {/* Poll Interval - only shown when polling continuously */}
          {!oneTimeOnly && (
            <StyledSelect
              label="Poll Interval"
              value={String(pollInterval)}
              onChange={(value) => updateApiConfig({ poll_interval_seconds: parseInt(value, 10) })}
              options={[
                { value: '10', label: '10 seconds' },
                { value: '30', label: '30 seconds' },
                { value: '60', label: '1 minute' },
                { value: '300', label: '5 minutes' },
                { value: '600', label: '10 minutes' },
              ]}
            />
          )}

          {/* Endpoints */}
          <div style={{ marginBottom: '12px' }}>
            <label
              style={{
                display: 'block',
                fontSize: '12px',
                fontWeight: 600,
                color: '#374151',
                marginBottom: '8px',
                textTransform: 'uppercase',
                letterSpacing: '0.025em',
              }}
            >
              Endpoints
            </label>

            {endpoints.length === 0 && (
              <p style={{ fontSize: '12px', color: '#9ca3af', fontStyle: 'italic', margin: '8px 0' }}>
                No endpoints configured. A default "/" endpoint will be used.
              </p>
            )}

            {endpoints.map((ep, idx) => (
              <div
                key={idx}
                style={{
                  padding: '12px',
                  backgroundColor: '#ffffff',
                  borderRadius: '6px',
                  border: '1px solid #e5e7eb',
                  marginBottom: '8px',
                }}
              >
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '8px' }}>
                  <span style={{ fontSize: '12px', fontWeight: 500, color: '#374151' }}>
                    Endpoint {idx + 1}
                  </span>
                  <button
                    onClick={() => removeEndpoint(idx)}
                    style={{
                      padding: '2px 8px',
                      backgroundColor: '#fef2f2',
                      color: '#dc2626',
                      border: '1px solid #fecaca',
                      borderRadius: '4px',
                      fontSize: '11px',
                      cursor: 'pointer',
                    }}
                  >
                    Remove
                  </button>
                </div>

                <input
                  type="text"
                  placeholder="/api/v2/forecast"
                  value={ep.path}
                  onChange={(e) => updateEndpoint(idx, 'path', e.target.value)}
                  style={{
                    width: '100%',
                    padding: '8px 10px',
                    marginBottom: '8px',
                    border: '1px solid #d1d5db',
                    borderRadius: '4px',
                    fontSize: '12px',
                    boxSizing: 'border-box',
                    color: '#374151',
                    backgroundColor: '#ffffff',
                  }}
                />

                <input
                  type="text"
                  placeholder="lat=59.9&lon=10.7"
                  value={ep.params || ''}
                  onChange={(e) => updateEndpoint(idx, 'params', e.target.value)}
                  style={{
                    width: '100%',
                    padding: '8px 10px',
                    marginBottom: '8px',
                    border: '1px solid #d1d5db',
                    borderRadius: '4px',
                    fontSize: '12px',
                    boxSizing: 'border-box',
                    color: '#374151',
                    backgroundColor: '#ffffff',
                  }}
                />
                <span style={{ fontSize: '10px', color: '#9ca3af', marginTop: '-6px', marginBottom: '6px', display: 'block' }}>Query params (e.g. lat=59.9&amp;lon=10.7)</span>

                <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
                  <select
                    value={ep.auth_type}
                    onChange={(e) => updateEndpoint(idx, 'auth_type', e.target.value)}
                    style={{
                      width: '100%',
                      padding: '8px 10px',
                      border: '1px solid #d1d5db',
                      borderRadius: '4px',
                      fontSize: '12px',
                      color: '#374151',
                      backgroundColor: '#ffffff',
                      boxSizing: 'border-box',
                    }}
                  >
                    <option value="none">No Auth</option>
                    <option value="bearer">Bearer Token</option>
                    <option value="api_key">API Key</option>
                    <option value="oauth">OAuth 2.0</option>
                  </select>

                  {(ep.auth_type === 'bearer' || ep.auth_type === 'api_key') && (
                    <SecretInput
                      label={ep.auth_type === 'bearer' ? 'Bearer token' : 'API key'}
                      placeholder={ep.auth_type === 'bearer' ? 'Token value' : 'API key value'}
                      field="auth_value"
                      config={ep as unknown as Record<string, unknown>}
                      defaultSecretName={`api-${ep.auth_type}-${idx}`}
                      onChange={(patch) => patchEndpoint(idx, patch)}
                    />
                  )}

                  {ep.auth_type === 'oauth' && (
                    <OAuthGrantSelector
                      value={ep.oauth_grant_id}
                      onChange={(grantId) => updateEndpoint(idx, 'oauth_grant_id', grantId || '')}
                    />
                  )}
                </div>
              </div>
            ))}

            <button
              onClick={addEndpoint}
              style={{
                width: '100%',
                padding: '8px 12px',
                backgroundColor: '#ffffff',
                border: '1px dashed #d1d5db',
                borderRadius: '6px',
                fontSize: '12px',
                color: '#6b7280',
                cursor: 'pointer',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                gap: '6px',
              }}
            >
              <span style={{ fontSize: '14px' }}>+</span> Add Endpoint
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

export default function PropertyEditor({
  node,
  onUpdate,
  onClose,
  onDelete,
  deployedConnectionId,
  allNodes,
  allEdges,
}: {
  node: Node
  onUpdate: (config: Record<string, unknown>) => void
  onClose: () => void
  onDelete?: () => void
  deployedConnectionId?: string
  allNodes?: Node[]
  allEdges?: Edge[]
}) {
  const [config, setConfig] = useState(node.data.config || {})
  const [closeHovered, setCloseHovered] = useState(false)
  const [tunnelUrl, setTunnelUrl] = useState('')
  const [tunnelLoading, setTunnelLoading] = useState(false)
  const [_testBody, _setTestBody] = useState('{\n  "test": true,\n  "message": "Hello from VRSky!"\n}')
  const [_testResponse, _setTestResponse] = useState<{ status: number; body: string } | null>(null)
  const [_testSending, _setTestSending] = useState(false)
  const [editingLabel, setEditingLabel] = useState(false)
  const [labelValue, setLabelValue] = useState(node.data.label || '')

  // Check tunnel status on mount
  useEffect(() => {
    fetch('http://localhost:9100/tunnel/status')
      .then(r => r.json())
      .then(d => { if (d.running && d.url) setTunnelUrl(d.url) })
      .catch(() => {})
  }, [])

  // Track which node we're editing to detect when it changes
  const currentNodeIdRef = useRef<string>(node.id)
  
  // Only sync config from node when we switch to editing a DIFFERENT node
  // This prevents the config from being overwritten when the same node re-renders
  useEffect(() => {
    if (currentNodeIdRef.current !== node.id) {
      // Different node selected - load its config
      setConfig(node.data.config || {})
      setLabelValue(node.data.label || '')
      setEditingLabel(false)
      currentNodeIdRef.current = node.id
    }
    // If same node, keep local config state (preserves unsaved changes)
  }, [node.id, node.data.config])

  const nodeType = node.type as string
  const nodeColor = NODE_COLORS[nodeType] || NODE_COLORS.consumer

  // Check if configuration is ready to save
  // Input and output nodes need a type selected, other nodes are always ready
  const isReadyToSave = (): boolean => {
    if (nodeType === 'input' || nodeType === 'output') {
      return Boolean(config.type)
    }
    return true // Filter and converter nodes are always ready
  }

  const renderConfigFields = () => {
    switch (nodeType) {
      case 'input':
        return (
          <div>
            <StyledSelect
              label="Source Type"
              value={(config.type as string) || ''}
              onChange={(value) => setConfig({ ...config, type: value })}
              options={[
                { value: '', label: 'Select source type...' },
                { value: 'api', label: 'API Input' },
                { value: 'http', label: 'Webhook (HTTP)' },
                { value: 'file', label: 'File Watcher' },
                { value: 'database', label: 'Database CDC' },
                { value: 'tenant', label: 'Tenant Input' },
                { value: 'salesforce', label: 'Salesforce' },
                { value: 'sftp', label: 'SFTP' },
                { value: 'kafka', label: 'Kafka' },
                { value: 'rabbitmq', label: 'RabbitMQ' },
                { value: 'cloud_storage', label: 'Cloud Storage (S3/Azure/GCS)' },
              ]}
            />

            {/* Help message when no source type selected */}
            {!config.type && (
              <div
                style={{
                  padding: '24px 16px',
                  backgroundColor: '#f9fafb',
                  borderRadius: '6px',
                  border: '1px solid #e5e7eb',
                  marginTop: '16px',
                  textAlign: 'center',
                }}
              >
                <p
                  style={{
                    fontSize: '13px',
                    color: '#9ca3af',
                    fontStyle: 'italic',
                    margin: 0,
                  }}
                >
                  Select a source type from the dropdown above to configure this input
                </p>
              </div>
            )}

            {config.type === 'http' && (
              <div className="space-y-3">
                <p className="text-xs text-neutral-500 dark:text-neutral-400">
                  Listens for incoming POST requests from external services like GitHub, Stripe, or Bruno. Click Connect to get a public URL.
                </p>

                {tunnelUrl ? (
                  <div className="space-y-3">
                    {/* Connection status */}
                    <div className="p-3 bg-green-50 dark:bg-green-900/20 border border-green-200 dark:border-green-800 rounded-md">
                      <div className="flex items-center justify-between mb-1">
                        <p className="text-xs text-green-600 dark:text-green-400 font-medium">Connected</p>
                        <button
                          onClick={async () => {
                            setTunnelLoading(true)
                            try {
                              await fetch('http://localhost:9100/tunnel/stop', { method: 'POST' })
                              setTunnelUrl('')
                              _setTestResponse(null)
                            } catch (e) { console.error(e) }
                            setTunnelLoading(false)
                          }}
                          disabled={tunnelLoading}
                          className="px-2 py-0.5 text-xs bg-red-500 text-white rounded hover:bg-red-600 disabled:opacity-50"
                        >
                          {tunnelLoading ? '...' : 'Disconnect'}
                        </button>
                      </div>
                      <div className="flex items-center gap-2">
                        <code className="flex-1 text-xs text-green-800 dark:text-green-200 bg-green-100 dark:bg-green-900/40 p-1.5 rounded font-mono break-all">
                          {tunnelUrl}/webhook/{deployedConnectionId || '<deploy first>'}
                        </code>
                        <button
                          onClick={() => {
                            navigator.clipboard.writeText(`${tunnelUrl}/webhook/${deployedConnectionId || ''}`)
                          }}
                          className="px-2 py-1 text-xs bg-green-600 text-white rounded hover:bg-green-700 whitespace-nowrap"
                        >
                          Copy
                        </button>
                      </div>
                    </div>

                  </div>
                ) : (
                  <button
                    onClick={async () => {
                      setTunnelLoading(true)
                      try {
                        const res = await fetch('http://localhost:9100/tunnel/start', { method: 'POST' })
                        const data = await res.json()
                        if (data.url) {
                          setTunnelUrl(data.url)
                        } else {
                          for (let i = 0; i < 10; i++) {
                            await new Promise(r => setTimeout(r, 2000))
                            const s = await fetch('http://localhost:9100/tunnel/status')
                            const st = await s.json()
                            if (st.url) { setTunnelUrl(st.url); break }
                          }
                        }
                      } catch (e) { console.error(e) }
                      setTunnelLoading(false)
                    }}
                    disabled={tunnelLoading}
                    className="w-full px-4 py-2 text-sm bg-blue-600 text-white rounded-md hover:bg-blue-700 disabled:opacity-50 font-medium"
                  >
                    {tunnelLoading ? 'Connecting...' : 'Connect'}
                  </button>
                )}

                <WebhookSignatureConfig
                  http={(config.http as Record<string, unknown>) || {}}
                  onChange={(nextHttp) => setConfig({ ...config, http: nextHttp })}
                />
              </div>
            )}

            {config.type === 'file' && (
              <div className="space-y-3">
                <StyledInput
                  label="Watch Directory"
                  placeholder="/home/user/my-data"
                  value={(config.file as any)?.path || ''}
                  onChange={(value) =>
                    setConfig({
                      ...config,
                      file: { ...(config.file as any), path: value },
                    })
                  }
                />
                {deployedConnectionId ? (
                  <div className="p-3 bg-green-50 dark:bg-green-900/20 border border-green-200 dark:border-green-800 rounded-md">
                    <p className="text-xs text-green-600 dark:text-green-300">
                      Watching for new files. You can also upload files via the panel below.
                    </p>
                  </div>
                ) : (
                  <p className="text-xs text-neutral-500 dark:text-neutral-400">
                    Specify a folder path on your machine. The pipeline will watch it for new files and process them automatically. You can also upload files via the UI after deploying.
                  </p>
                )}
              </div>
            )}

            {config.type === 'database' && (
              <DatabaseConsumerConfig
                config={config}
                setConfig={setConfig}
                deployedConnectionId={deployedConnectionId}
              />
            )}

            {config.type === 'api' && (
              <ApiConsumerConfig
                config={config}
                setConfig={setConfig}
              />
            )}

            {config.type === 'tenant' && (
              <TenantConsumerConfig
                config={config}
                setConfig={setConfig}
              />
            )}

            {config.type === 'salesforce' && (
              <SalesforceConsumerConfig
                config={config}
                setConfig={setConfig}
                deployedConnectionId={deployedConnectionId}
              />
            )}

            {config.type === 'sftp' && (
              <SFTPConsumerConfig config={config} setConfig={setConfig} />
            )}

            {config.type === 'kafka' && (
              <KafkaConfigEditor config={config} setConfig={setConfig} role="consumer" />
            )}

            {config.type === 'rabbitmq' && (
              <RabbitMQConfigEditor config={config} setConfig={setConfig} role="consumer" />
            )}

            {config.type === 'cloud_storage' && (
              <CloudStorageConfigEditor config={config} setConfig={setConfig} role="consumer" />
            )}
          </div>
        )

      case 'output':
        return (
          <div>
            <StyledSelect
              label="Destination Type"
              value={(config.type as string) || ''}
              onChange={(value) => setConfig({ ...config, type: value })}
              options={[
                { value: '', label: 'Select destination type...' },
                { value: 'http', label: 'Webhook (HTTP)' },
                { value: 'file', label: 'File Output' },
                { value: 'database', label: 'Database' },
                { value: 'salesforce', label: 'Salesforce' },
                { value: 'sftp', label: 'SFTP' },
                { value: 'kafka', label: 'Kafka' },
                { value: 'rabbitmq', label: 'RabbitMQ' },
                { value: 'cloud_storage', label: 'Cloud Storage (S3/Azure/GCS)' },
              ]}
            />

            {/* Help message when no destination type selected */}
            {!config.type && (
              <div
                style={{
                  padding: '24px 16px',
                  backgroundColor: '#f9fafb',
                  borderRadius: '6px',
                  border: '1px solid #e5e7eb',
                  marginTop: '16px',
                  textAlign: 'center',
                }}
              >
                <p
                  style={{
                    fontSize: '13px',
                    color: '#9ca3af',
                    fontStyle: 'italic',
                    margin: 0,
                  }}
                >
                  Select a destination type from the dropdown above to configure this output
                </p>
              </div>
            )}

            {config.type === 'http' && (
              <>
                <StyledInput
                  label="Destination URL"
                  placeholder="https://example.com/api/webhook"
                  value={(config.http as any)?.url || ''}
                  onChange={(value) =>
                    setConfig({
                      ...config,
                      http: { ...(config.http as any), url: value },
                    })
                  }
                />
                <StyledSelect
                  label="HTTP Method"
                  value={(config.http as any)?.method || 'POST'}
                  onChange={(value) =>
                    setConfig({
                      ...config,
                      http: { ...(config.http as any), method: value },
                    })
                  }
                  options={[
                    { value: 'POST', label: 'POST' },
                    { value: 'PUT', label: 'PUT' },
                    { value: 'PATCH', label: 'PATCH' },
                  ]}
                />
                <p className="text-xs text-neutral-500 dark:text-neutral-400 mt-1">
                  Pipeline data will be POSTed to this URL. Paste a webhook URL from any external service (e.g. webhook.site, Slack, Discord).
                </p>

                <StyledSelect
                  label="Authentication"
                  value={((config.http as any)?.auth_type as string) || 'none'}
                  onChange={(value) =>
                    setConfig({ ...config, http: { ...(config.http as any), auth_type: value } })
                  }
                  options={[
                    { value: 'none', label: 'None' },
                    { value: 'oauth', label: 'OAuth 2.0' },
                  ]}
                />
                {(config.http as any)?.auth_type === 'oauth' && (
                  <div style={{ marginTop: '4px' }}>
                    <OAuthGrantSelector
                      value={(config.http as any)?.oauth_grant_id || ''}
                      onChange={(grantId) =>
                        setConfig({
                          ...config,
                          http: { ...(config.http as any), oauth_grant_id: grantId || '' },
                        })
                      }
                      connectionId={deployedConnectionId}
                    />
                  </div>
                )}
              </>
            )}

            {config.type === 'file' && (
              <StyledInput
                label="Output Directory"
                placeholder="/tmp/output"
                value={(config.file as any)?.path || ''}
                onChange={(value) =>
                  setConfig({
                    ...config,
                    file: { ...(config.file as any), path: value },
                  })
                }
              />
            )}

            {config.type === 'database' && (
              <DatabaseProducerConfig
                config={config}
                setConfig={setConfig}
              />
            )}

            {config.type === 'salesforce' && (
              <SalesforceProducerConfig
                config={config}
                setConfig={setConfig}
                deployedConnectionId={deployedConnectionId}
              />
            )}

            {config.type === 'sftp' && (
              <SFTPProducerConfig config={config} setConfig={setConfig} />
            )}

            {config.type === 'kafka' && (
              <KafkaConfigEditor config={config} setConfig={setConfig} role="producer" />
            )}

            {config.type === 'rabbitmq' && (
              <RabbitMQConfigEditor config={config} setConfig={setConfig} role="producer" />
            )}

            {config.type === 'cloud_storage' && (
              <CloudStorageConfigEditor config={config} setConfig={setConfig} role="producer" />
            )}
          </div>
        )

      case 'converter':
        return (
          <ConverterConfig config={config} setConfig={setConfig} allNodes={allNodes} allEdges={allEdges} currentNodeId={node.id} />
        )

      case 'filter':
        return (
          <FilterConfig config={config} setConfig={setConfig} allNodes={allNodes} allEdges={allEdges} currentNodeId={node.id} deployedConnectionId={deployedConnectionId} />
        )

      default:
        return (
          <p style={{ fontSize: '13px', color: '#6b7280' }}>
            No configuration available
          </p>
        )
    }
  }

  return (
    <div
      style={{
        height: '100%',
        backgroundColor: '#ffffff',
        display: 'flex',
        flexDirection: 'column',
      }}
    >
      {/* Header */}
      <div
        style={{
          padding: '16px',
          backgroundColor: nodeColor.bg,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          borderBottom: '1px solid rgba(0, 0, 0, 0.08)',
        }}
      >
        <h3
          style={{
            margin: 0,
            fontSize: '15px',
            fontWeight: 600,
            color: nodeColor.text,
          }}
        >
          {editingLabel ? (
            <input
              autoFocus
              value={labelValue}
              onChange={(e) => setLabelValue(e.target.value)}
              onBlur={() => setEditingLabel(false)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') setEditingLabel(false)
                if (e.key === 'Escape') { setLabelValue(node.data.label || ''); setEditingLabel(false) }
              }}
              style={{
                fontSize: '15px',
                fontWeight: 600,
                color: nodeColor.text,
                background: 'rgba(255,255,255,0.3)',
                border: '1px solid rgba(0,0,0,0.2)',
                borderRadius: '4px',
                padding: '1px 6px',
                outline: 'none',
                width: '100%',
              }}
            />
          ) : (
            <span
              onClick={() => setEditingLabel(true)}
              style={{ cursor: 'pointer' }}
              title="Click to rename"
            >
              {labelValue || node.data.label}
            </span>
          )}
        </h3>
        <button
          onClick={onClose}
          onMouseEnter={() => setCloseHovered(true)}
          onMouseLeave={() => setCloseHovered(false)}
          style={{
            width: '28px',
            height: '28px',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            backgroundColor: closeHovered ? 'rgba(0, 0, 0, 0.1)' : 'transparent',
            border: 'none',
            borderRadius: '4px',
            cursor: 'pointer',
            color: nodeColor.text,
            fontSize: '16px',
            fontWeight: 500,
            transition: 'all 150ms ease',
          }}
          title="Close"
        >
          X
        </button>
      </div>

      {/* Config Fields */}
      <div
        style={{
          flex: 1,
          overflowY: 'auto',
          padding: '16px',
        }}
      >
        {renderConfigFields()}
      </div>

      {/* Footer Actions */}
      <div
        style={{
          padding: '16px',
          borderTop: '1px solid #e5e7eb',
          backgroundColor: '#f9fafb',
          display: 'flex',
          flexDirection: 'column',
          gap: '8px',
        }}
      >
        <StyledButton
          onClick={() => {
            onUpdate(labelValue !== node.data.label ? { ...config, _label: labelValue } : config)
            onClose()
          }}
          variant="primary"
          disabled={!isReadyToSave()}
        >
          Save Configuration
        </StyledButton>

        {onDelete && (
          <StyledButton onClick={onDelete} variant="danger">
            Delete Node
          </StyledButton>
        )}
      </div>
    </div>
  )
}
