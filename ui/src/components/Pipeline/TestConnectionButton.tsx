// TestConnectionButton (#82): a shared "Test connection" control for connector
// config editors. It POSTs the DRAFT node config to the management-api
// /api/v1/connections/test (which dispatches to the right worker), and shows a
// green "Connected" (+ sample) or red error. Nothing is persisted.
import { useState } from 'react'
import apiClient from '../../services/api'

interface TestResult {
  ok?: boolean
  error?: string
  sample?: unknown[]
  tables?: string[]
  fields?: Array<{ name: string }>
  partitions?: number
}

export default function TestConnectionButton({
  config,
  role = 'consumer',
  disabled,
}: {
  /** The node config object (e.g. { type:'database', database:{…} }). */
  config: Record<string, unknown>
  role?: 'consumer' | 'producer'
  disabled?: boolean
}) {
  const [testing, setTesting] = useState(false)
  const [result, setResult] = useState<TestResult | null>(null)

  const run = async () => {
    setTesting(true)
    setResult(null)
    try {
      const resp = await apiClient.post('/api/v1/connections/test', { ...config, role })
      setResult(resp.data as TestResult)
    } catch (err) {
      setResult({ ok: false, error: err instanceof Error ? err.message : 'Test failed' })
    }
    setTesting(false)
  }

  // Summarize whatever sample the worker returned (objects/tables/fields/…).
  const summary = (): string | null => {
    if (!result?.ok) return null
    if (result.tables?.length) return `Tables: ${result.tables.slice(0, 8).join(', ')}`
    if (result.fields?.length) return `${result.fields.length} fields`
    if (typeof result.partitions === 'number') return `${result.partitions} partitions`
    if (result.sample?.length) {
      const items = result.sample.slice(0, 5).map((s) => (typeof s === 'string' ? s : JSON.stringify(s)))
      return `${result.sample.length} item(s): ${items.join(', ')}`
    }
    return null
  }

  return (
    <div>
      <button
        type="button"
        onClick={run}
        disabled={testing || disabled}
        style={{
          width: '100%', padding: '8px 12px', fontSize: '13px', fontWeight: 600,
          backgroundColor: testing || disabled ? '#9ca3af' : '#2563eb', color: '#fff',
          border: 'none', borderRadius: '6px', cursor: testing || disabled ? 'not-allowed' : 'pointer',
        }}
      >
        {testing ? 'Testing…' : 'Test connection'}
      </button>
      {result && (
        <div
          style={{
            marginTop: '6px', padding: '8px 12px', borderRadius: '6px', fontSize: '12px',
            backgroundColor: result.ok ? '#f0fdf4' : '#fef2f2',
            border: `1px solid ${result.ok ? '#bbf7d0' : '#fecaca'}`,
            color: result.ok ? '#166534' : '#991b1b',
            whiteSpace: 'pre-wrap', wordBreak: 'break-word',
          }}
        >
          {result.ok ? (
            <>
              <strong>Connected!</strong>
              {summary() && <div style={{ marginTop: '2px' }}>{summary()}</div>}
            </>
          ) : (
            <>{result.error || 'Connection failed'}</>
          )}
        </div>
      )}
    </div>
  )
}
