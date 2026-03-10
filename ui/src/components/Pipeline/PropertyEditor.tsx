import { useState } from 'react'
import type { Node } from '../../types/pipeline'

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
}: {
  children: React.ReactNode
  onClick: () => void
  variant?: 'primary' | 'danger'
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
  }

  const colorSet = colors[variant]

  return (
    <button
      onClick={onClick}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      style={{
        width: '100%',
        padding: '10px 16px',
        backgroundColor: hovered ? colorSet.hoverBg : colorSet.bg,
        color: colorSet.text,
        border: '1px solid rgba(0, 0, 0, 0.08)',
        borderRadius: '6px',
        fontSize: '13px',
        fontWeight: 500,
        cursor: 'pointer',
        transition: 'all 150ms ease',
        transform: hovered ? 'scale(1.02)' : 'scale(1)',
        boxShadow: hovered ? '0 2px 4px rgba(0, 0, 0, 0.1)' : '0 1px 2px rgba(0, 0, 0, 0.05)',
      }}
    >
      {children}
    </button>
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

  const nodeType = node.type as string
  const nodeColor = NODE_COLORS[nodeType] || NODE_COLORS.consumer

  const renderConfigFields = () => {
    switch (nodeType) {
      case 'consumer':
        return (
          <div>
            <StyledSelect
              label="Source Type"
              value={(config.type as string) || 'http'}
              onChange={(value) => setConfig({ ...config, type: value })}
              options={[
                { value: 'http', label: 'HTTP Webhook' },
                { value: 'file', label: 'File Watcher' },
                { value: 'database', label: 'Database CDC' },
              ]}
            />

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
          </div>
        )

      case 'producer':
        return (
          <div>
            <StyledSelect
              label="Destination Type"
              value={(config.type as string) || 'http'}
              onChange={(value) => setConfig({ ...config, type: value })}
              options={[
                { value: 'http', label: 'HTTP API' },
                { value: 'file', label: 'File Output' },
                { value: 'database', label: 'Database' },
              ]}
            />

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
