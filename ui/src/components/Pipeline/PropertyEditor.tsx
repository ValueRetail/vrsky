import { useState, useEffect, useRef } from 'react'
import type { Node } from '../../types/pipeline'
import { useAuthStore } from '../../store/authStore'
import * as tenantDataService from '../../services/tenantDataService'
import type { TenantDataConnection, DataConnectionRequest } from '../../types/models'

// Type for API Consumer endpoint
interface ApiEndpoint {
  path: string
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
        <StyledSelect
          label="Data Connection"
          value={(tenant.connection_id as string) || ''}
          onChange={(value) => setConfig({ ...config, tenant: { ...tenant, connection_id: value } })}
          options={[
            { value: '', label: 'Select a connection...' },
            ...connections.map(c => ({
              value: c.id,
              label: `${c.requester_tenant_id === currentTenant?.id ? 'To' : 'From'}: ${c.requester_tenant_id === currentTenant?.id ? c.target_tenant_id.slice(0, 8) : c.requester_tenant_id.slice(0, 8)}... (${c.permission_type})`,
            })),
          ]}
        />
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
    const newEndpoints = [...endpoints, { path: '/', auth_type: 'none' as const, auth_value: '' }]
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
}: {
  node: Node
  onUpdate: (config: Record<string, unknown>) => void
  onClose: () => void
  onDelete?: () => void
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
              <>
                <StyledInput
                  label="Webhook URL"
                  placeholder="https://example.com/webhook"
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
                    { value: 'GET', label: 'GET' },
                    { value: 'PUT', label: 'PUT' },
                  ]}
                />
              </>
            )}

            {config.type === 'file' && (
              <StyledInput
                label="Watch Directory"
                placeholder="/tmp/input"
                value={(config.file as any)?.path || ''}
                onChange={(value) =>
                  setConfig({
                    ...config,
                    file: { ...(config.file as any), path: value },
                  })
                }
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
          </div>
        )

      case 'filter':
      case 'converter':
        return (
          <div
            style={{
              padding: '16px',
              backgroundColor: '#f9fafb',
              borderRadius: '6px',
              border: '1px solid #e5e7eb',
            }}
          >
            <p
              style={{
                fontSize: '13px',
                color: '#6b7280',
                fontStyle: 'italic',
                margin: 0,
                textAlign: 'center',
              }}
            >
              Configuration coming soon
            </p>
          </div>
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
