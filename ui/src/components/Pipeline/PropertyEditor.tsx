import { useState, useEffect, useRef } from 'react'
import type { Node } from '../../types/pipeline'
import { useAuthStore } from '../../store/authStore'
import * as tenantDataService from '../../services/tenantDataService'
import type { TenantDataConnection, DataConnectionRequest } from '../../types/models'

// Type for API Consumer endpoint
interface ApiEndpoint {
  path: string
  params: string
  auth_type: 'none' | 'bearer' | 'api_key'
  auth_value: string
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
        <StyledInput
          label="Password"
          placeholder="••••••••"
          value={password}
          onChange={(v) => updateDB({ password: v })}
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
        <StyledInput
          label="Password"
          placeholder="••••••••"
          value={password}
          onChange={(v) => updateDB({ password: v })}
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
}: {
  config: Record<string, unknown>
  setConfig: (c: Record<string, unknown>) => void
}) {
  const rules = (config.rules as Array<{ field: string; operator: string; value: string }>) || []
  const logic = (config.logic as string) || 'and'
  const extractFields = (config.extract_fields as string[]) || []
  const flattenPath = (config.flatten_path as string) || ''
  const flattenFields = (config.flatten_fields as Record<string, string>) || {}
  const flattenInclude = (config.flatten_include as Record<string, string>) || {}

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

  return (
    <div>
      {/* Logic toggle */}
      <div style={{ marginBottom: '12px', display: 'flex', alignItems: 'center', gap: '8px' }}>
        <span style={{ fontSize: '12px', fontWeight: 600, color: '#374151' }}>Match:</span>
        {['and', 'or'].map((l) => (
          <button
            key={l}
            onClick={() => setConfig({ ...config, logic: l })}
            style={{
              padding: '4px 12px', fontSize: '12px', fontWeight: 600,
              backgroundColor: logic === l ? (l === 'and' ? '#dbeafe' : '#fef3c7') : '#f3f4f6',
              color: logic === l ? (l === 'and' ? '#1d4ed8' : '#92400e') : '#6b7280',
              border: `1px solid ${logic === l ? (l === 'and' ? '#93c5fd' : '#fcd34d') : '#d1d5db'}`,
              borderRadius: '4px', cursor: 'pointer',
            }}
          >
            {l.toUpperCase()}
          </button>
        ))}
        <span style={{ fontSize: '11px', color: '#9ca3af' }}>
          {logic === 'and' ? 'All rules must match' : 'Any rule can match'}
        </span>
      </div>

      {/* Rules */}
      {rules.map((rule, i) => (
        <div key={i} style={{
          display: 'flex', gap: '6px', alignItems: 'center', marginBottom: '8px',
          padding: '8px', backgroundColor: '#f9fafb', borderRadius: '6px', border: '1px solid #e5e7eb',
        }}>
          <input
            placeholder="field"
            value={rule.field}
            onChange={(e) => updateRule(i, 'field', e.target.value)}
            style={{
              flex: 1, padding: '6px 8px', fontSize: '12px', border: '1px solid #d1d5db',
              borderRadius: '4px', fontFamily: 'monospace',
            }}
          />
          <select
            value={rule.operator}
            onChange={(e) => updateRule(i, 'operator', e.target.value)}
            style={{
              padding: '6px 4px', fontSize: '12px', border: '1px solid #d1d5db',
              borderRadius: '4px', backgroundColor: 'white',
            }}
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
              style={{
                flex: 1, padding: '6px 8px', fontSize: '12px', border: '1px solid #d1d5db',
                borderRadius: '4px',
              }}
            />
          )}
          <button
            onClick={() => removeRule(i)}
            style={{
              padding: '4px 8px', fontSize: '14px', background: 'none', border: 'none',
              color: '#dc2626', cursor: 'pointer',
            }}
          >
            ×
          </button>
        </div>
      ))}

      <button
        onClick={addRule}
        style={{
          padding: '6px 12px', fontSize: '12px', fontWeight: 600,
          backgroundColor: '#f3f4f6', border: '1px solid #d1d5db',
          borderRadius: '4px', cursor: 'pointer', color: '#374151',
        }}
      >
        + Add Rule
      </button>

      {rules.length === 0 && !extractFields.length && (
        <p style={{ fontSize: '12px', color: '#9ca3af', fontStyle: 'italic', marginTop: '8px' }}>
          No filter rules configured. Add rules to filter rows, or extract fields to pick specific data.
        </p>
      )}

      {/* Extract Fields */}
      <div style={{ marginTop: '16px', borderTop: '1px solid #e5e7eb', paddingTop: '12px' }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '8px' }}>
          <span style={{ fontSize: '12px', fontWeight: 600, color: '#374151' }}>Extract Fields</span>
          <span style={{ fontSize: '11px', color: '#9ca3af' }}>Keep only these JSON paths</span>
        </div>
        {extractFields.map((field: string, i: number) => (
          <div key={i} style={{
            display: 'flex', gap: '6px', alignItems: 'center', marginBottom: '6px',
          }}>
            <input
              placeholder="e.g. properties.timeseries.data.instant.details.air_temperature"
              value={field}
              onChange={(e) => {
                const newFields = [...extractFields]
                newFields[i] = e.target.value
                setConfig({ ...config, extract_fields: newFields })
              }}
              style={{
                flex: 1, padding: '6px 8px', fontSize: '12px', border: '1px solid #d1d5db',
                borderRadius: '4px', fontFamily: 'monospace',
              }}
            />
            <button
              onClick={() => setConfig({ ...config, extract_fields: extractFields.filter((_: string, j: number) => j !== i) })}
              style={{
                padding: '4px 8px', fontSize: '14px', background: 'none', border: 'none',
                color: '#dc2626', cursor: 'pointer',
              }}
            >
              ×
            </button>
          </div>
        ))}
        <button
          onClick={() => setConfig({ ...config, extract_fields: [...extractFields, ''] })}
          style={{
            padding: '6px 12px', fontSize: '12px', fontWeight: 600,
            backgroundColor: '#f3f4f6', border: '1px solid #d1d5db',
            borderRadius: '4px', cursor: 'pointer', color: '#374151',
          }}
        >
          + Add Field
        </button>
        {extractFields.length > 0 && (
          <p style={{ fontSize: '11px', color: '#9ca3af', marginTop: '6px' }}>
            Use dot notation for nested paths. Arrays are traversed automatically.
            <br />Example: <code style={{ background: '#f3f4f6', padding: '1px 4px', borderRadius: '2px' }}>geometry.coordinates</code>, <code style={{ background: '#f3f4f6', padding: '1px 4px', borderRadius: '2px' }}>properties.timeseries.time</code>
          </p>
        )}
      </div>

      {/* Flatten Array */}
      <div style={{ marginTop: '16px', borderTop: '1px solid #e5e7eb', paddingTop: '12px' }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '8px' }}>
          <span style={{ fontSize: '12px', fontWeight: 600, color: '#374151' }}>Flatten Array</span>
          <span style={{ fontSize: '11px', color: '#9ca3af' }}>Unroll nested array into flat rows</span>
        </div>

        <div style={{ marginBottom: '8px' }}>
          <label style={{ fontSize: '11px', fontWeight: 600, color: '#6b7280', display: 'block', marginBottom: '4px' }}>Array Path</label>
          <input
            placeholder="e.g. properties.timeseries"
            value={flattenPath}
            onChange={(e) => setConfig({ ...config, flatten_path: e.target.value })}
            style={{
              width: '100%', padding: '6px 8px', fontSize: '12px', border: '1px solid #d1d5db',
              borderRadius: '4px', fontFamily: 'monospace', boxSizing: 'border-box',
            }}
          />
        </div>

        {flattenPath && (
          <>
            <div style={{ marginBottom: '8px' }}>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '4px' }}>
                <label style={{ fontSize: '11px', fontWeight: 600, color: '#6b7280' }}>Row Fields</label>
                <span style={{ fontSize: '10px', color: '#9ca3af' }}>Fields from each array element</span>
              </div>
              {Object.entries(flattenFields).map(([path, name], i) => (
                <div key={i} style={{ display: 'flex', gap: '4px', alignItems: 'center', marginBottom: '4px' }}>
                  <input
                    placeholder="source path"
                    value={path}
                    onChange={(e) => {
                      const newFields = { ...flattenFields }
                      delete newFields[path]
                      newFields[e.target.value] = name
                      setConfig({ ...config, flatten_fields: newFields })
                    }}
                    style={{ flex: 1, padding: '5px 6px', fontSize: '11px', border: '1px solid #d1d5db', borderRadius: '4px', fontFamily: 'monospace' }}
                  />
                  <span style={{ fontSize: '11px', color: '#9ca3af' }}>→</span>
                  <input
                    placeholder="output name"
                    value={name}
                    onChange={(e) => {
                      setConfig({ ...config, flatten_fields: { ...flattenFields, [path]: e.target.value } })
                    }}
                    style={{ flex: 1, padding: '5px 6px', fontSize: '11px', border: '1px solid #d1d5db', borderRadius: '4px', fontFamily: 'monospace' }}
                  />
                  <button
                    onClick={() => {
                      const newFields = { ...flattenFields }
                      delete newFields[path]
                      setConfig({ ...config, flatten_fields: newFields })
                    }}
                    style={{ padding: '2px 6px', fontSize: '14px', background: 'none', border: 'none', color: '#dc2626', cursor: 'pointer' }}
                  >×</button>
                </div>
              ))}
              <button
                onClick={() => setConfig({ ...config, flatten_fields: { ...flattenFields, '': '' } })}
                style={{ padding: '4px 10px', fontSize: '11px', fontWeight: 600, backgroundColor: '#f3f4f6', border: '1px solid #d1d5db', borderRadius: '4px', cursor: 'pointer', color: '#374151' }}
              >+ Add Row Field</button>
            </div>

            <div style={{ marginBottom: '8px' }}>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '4px' }}>
                <label style={{ fontSize: '11px', fontWeight: 600, color: '#6b7280' }}>Include from Parent</label>
                <span style={{ fontSize: '10px', color: '#9ca3af' }}>Fields from root added to every row</span>
              </div>
              {Object.entries(flattenInclude).map(([path, name], i) => (
                <div key={i} style={{ display: 'flex', gap: '4px', alignItems: 'center', marginBottom: '4px' }}>
                  <input
                    placeholder="e.g. geometry.coordinates[0]"
                    value={path}
                    onChange={(e) => {
                      const newInc = { ...flattenInclude }
                      delete newInc[path]
                      newInc[e.target.value] = name
                      setConfig({ ...config, flatten_include: newInc })
                    }}
                    style={{ flex: 1, padding: '5px 6px', fontSize: '11px', border: '1px solid #d1d5db', borderRadius: '4px', fontFamily: 'monospace' }}
                  />
                  <span style={{ fontSize: '11px', color: '#9ca3af' }}>→</span>
                  <input
                    placeholder="output name"
                    value={name}
                    onChange={(e) => {
                      setConfig({ ...config, flatten_include: { ...flattenInclude, [path]: e.target.value } })
                    }}
                    style={{ flex: 1, padding: '5px 6px', fontSize: '11px', border: '1px solid #d1d5db', borderRadius: '4px', fontFamily: 'monospace' }}
                  />
                  <button
                    onClick={() => {
                      const newInc = { ...flattenInclude }
                      delete newInc[path]
                      setConfig({ ...config, flatten_include: newInc })
                    }}
                    style={{ padding: '2px 6px', fontSize: '14px', background: 'none', border: 'none', color: '#dc2626', cursor: 'pointer' }}
                  >×</button>
                </div>
              ))}
              <button
                onClick={() => setConfig({ ...config, flatten_include: { ...flattenInclude, '': '' } })}
                style={{ padding: '4px 10px', fontSize: '11px', fontWeight: 600, backgroundColor: '#f3f4f6', border: '1px solid #d1d5db', borderRadius: '4px', cursor: 'pointer', color: '#374151' }}
              >+ Add Parent Field</button>
            </div>

            <p style={{ fontSize: '11px', color: '#9ca3af', marginTop: '4px' }}>
              Use <code style={{ background: '#f3f4f6', padding: '1px 4px', borderRadius: '2px' }}>[0]</code> for array indexes in parent paths.
              Example: <code style={{ background: '#f3f4f6', padding: '1px 4px', borderRadius: '2px' }}>geometry.coordinates[1]</code> → <code style={{ background: '#f3f4f6', padding: '1px 4px', borderRadius: '2px' }}>lat</code>
            </p>
          </>
        )}
      </div>
    </div>
  )
}

function ConverterConfig({
  config,
  setConfig,
  allNodes,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
  allNodes?: Node[]
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

  // Get consumer node config for fetching sample data
  const consumerNode = allNodes?.find(n => n.type === 'consumer')
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
              {fetchingSample ? 'Fetching...' : 'Fetch from Consumer'}
            </button>
          )}
        </div>
        <textarea
          value={previewInput}
          onChange={(e) => setPreviewInput(e.target.value)}
          style={{ width: '100%', height: '80px', padding: '6px 8px', fontSize: '11px', fontFamily: 'monospace', border: '1px solid #d1d5db', borderRadius: '4px', resize: 'vertical', boxSizing: 'border-box' }}
          placeholder={consumerType === 'database' ? 'Click "Fetch from Consumer" to load real data, or paste JSON here' : '[{"field": "value"}]'}
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
                  }}
                />
                <span style={{ fontSize: '10px', color: '#9ca3af', marginTop: '-6px', marginBottom: '6px', display: 'block' }}>Query params (e.g. lat=59.9&amp;lon=10.7)</span>

                <div style={{ display: 'flex', gap: '8px' }}>
                  <select
                    value={ep.auth_type}
                    onChange={(e) => updateEndpoint(idx, 'auth_type', e.target.value)}
                    style={{
                      flex: 1,
                      padding: '8px 10px',
                      border: '1px solid #d1d5db',
                      borderRadius: '4px',
                      fontSize: '12px',
                    }}
                  >
                    <option value="none">No Auth</option>
                    <option value="bearer">Bearer Token</option>
                    <option value="api_key">API Key</option>
                  </select>

                  {ep.auth_type !== 'none' && (
                    <input
                      type="text"
                      placeholder={ep.auth_type === 'bearer' ? 'Token' : 'API Key'}
                      value={ep.auth_value}
                      onChange={(e) => updateEndpoint(idx, 'auth_value', e.target.value)}
                      style={{
                        flex: 2,
                        padding: '8px 10px',
                        border: '1px solid #d1d5db',
                        borderRadius: '4px',
                        fontSize: '12px',
                      }}
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
}: {
  node: Node
  onUpdate: (config: Record<string, unknown>) => void
  onClose: () => void
  onDelete?: () => void
  deployedConnectionId?: string
  allNodes?: Node[]
}) {
  const [config, setConfig] = useState(node.data.config || {})
  const [closeHovered, setCloseHovered] = useState(false)
  
  // Track which node we're editing to detect when it changes
  const currentNodeIdRef = useRef<string>(node.id)
  
  // Only sync config from node when we switch to editing a DIFFERENT node
  // This prevents the config from being overwritten when the same node re-renders
  useEffect(() => {
    if (currentNodeIdRef.current !== node.id) {
      // Different node selected - load its config
      setConfig(node.data.config || {})
      currentNodeIdRef.current = node.id
    }
    // If same node, keep local config state (preserves unsaved changes)
  }, [node.id, node.data.config])

  const nodeType = node.type as string
  const nodeColor = NODE_COLORS[nodeType] || NODE_COLORS.consumer

  // Check if configuration is ready to save
  // Consumer and producer nodes need a type selected, other nodes are always ready
  const isReadyToSave = (): boolean => {
    if (nodeType === 'consumer' || nodeType === 'producer') {
      return Boolean(config.type)
    }
    return true // Filter and converter nodes are always ready
  }

  const renderConfigFields = () => {
    switch (nodeType) {
      case 'consumer':
        return (
          <div>
            <StyledSelect
              label="Source Type"
              value={(config.type as string) || ''}
              onChange={(value) => setConfig({ ...config, type: value })}
              options={[
                { value: '', label: 'Select source type...' },
                { value: 'api', label: 'API Consumer' },
                { value: 'http', label: 'HTTP Webhook' },
                { value: 'file', label: 'File Watcher' },
                { value: 'database', label: 'Database CDC' },
                { value: 'tenant', label: 'Tenant Consumer' },
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
                  Select a source type from the dropdown above to configure this consumer
                </p>
              </div>
            )}

            {config.type === 'http' && (
              <div className="p-3 bg-blue-50 dark:bg-blue-900/20 border border-blue-200 dark:border-blue-800 rounded-md">
                <p className="text-sm font-medium text-blue-800 dark:text-blue-200 mb-1">Inbound Webhook</p>
                {deployedConnectionId ? (
                  <>
                    <p className="text-xs text-green-600 dark:text-green-400 font-medium mb-1">Live webhook URL:</p>
                    <code className="block text-xs text-blue-800 dark:text-blue-200 bg-blue-100 dark:bg-blue-900/40 p-2 rounded font-mono break-all">
                      POST http://localhost:9100/webhook/{deployedConnectionId}
                    </code>
                  </>
                ) : (
                  <>
                    <p className="text-xs text-blue-600 dark:text-blue-300">
                      Deploy this pipeline to get your webhook URL. External services can POST data to it and it will flow through the pipeline.
                    </p>
                    <p className="text-xs text-blue-500 dark:text-blue-400 mt-2 font-mono">
                      POST http://localhost:9100/webhook/{'<connection-id>'}
                    </p>
                  </>
                )}
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
          </div>
        )

      case 'producer':
        return (
          <div>
            <StyledSelect
              label="Destination Type"
              value={(config.type as string) || ''}
              onChange={(value) => setConfig({ ...config, type: value })}
              options={[
                { value: '', label: 'Select destination type...' },
                { value: 'http', label: 'HTTP API' },
                { value: 'file', label: 'File Output' },
                { value: 'database', label: 'Database' },
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
                  Select a destination type from the dropdown above to configure this producer
                </p>
              </div>
            )}

            {config.type === 'http' && (
              <>
                <StyledInput
                  label="Target URL"
                  placeholder="https://example.com/api"
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
                  Data flowing through the pipeline will be forwarded to this URL.
                  For testing, use <code className="bg-neutral-100 dark:bg-neutral-800 px-1 rounded">http://httpbin:80/post</code>
                </p>
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
          </div>
        )

      case 'converter':
        return (
          <ConverterConfig config={config} setConfig={setConfig} allNodes={allNodes} />
        )

      case 'filter':
        return (
          <FilterConfig config={config} setConfig={setConfig} />
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
          {node.data.label}
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
            onUpdate(config)
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
