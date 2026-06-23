/**
 * First-login onboarding wizard (Phase 4B / #93).
 *
 * Walks a non-developer from a template gallery to a deployed, running pipeline
 * in a few steps: pick a template → fill the handful of fields it needs (mostly
 * credentials) → deploy → see it work. It reuses the same deploy path as the
 * visual builder (createConnection → materializeSecrets → start), so nothing
 * plaintext is persisted and the result is a normal connection.
 */

import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import apiClient from '../services/api'
import { config } from '../config/env'
import { useAuthStore } from '../store/authStore'
import { materializeSecrets } from '../utils/secrets'
import SecretInput from '../components/Pipeline/SecretInput'
import { TEMPLATES, type PipelineTemplate, type TemplateField } from './templates'
import { markOnboarded } from './state'

// --- nested config helpers ------------------------------------------------

// getObj resolves (and creates) the nested object at objectPath. It MUTATES
// root, so only call it on a cloned config inside a setConfigs updater — never
// during render or on a state object directly.
function getObj(root: Record<string, unknown>, objectPath: string): Record<string, unknown> {
  if (!objectPath) return root
  let cur = root
  for (const part of objectPath.split('.')) {
    if (typeof cur[part] !== 'object' || cur[part] === null) cur[part] = {}
    cur = cur[part] as Record<string, unknown>
  }
  return cur
}

// readObj is the non-mutating counterpart used during render and in derived
// state: it never creates intermediate objects (so it can't mutate React
// state), returning an empty object when the path is absent.
function readObj(root: Record<string, unknown>, objectPath: string): Record<string, unknown> {
  if (!objectPath) return root
  let cur: unknown = root
  for (const part of objectPath.split('.')) {
    if (!cur || typeof cur !== 'object') return {}
    cur = (cur as Record<string, unknown>)[part]
  }
  return cur && typeof cur === 'object' ? (cur as Record<string, unknown>) : {}
}

// --- styles (inline, matching the rest of the app) ------------------------

const page: React.CSSProperties = { maxWidth: '860px', margin: '0 auto', padding: '40px 20px' }
const card: React.CSSProperties = { background: '#fff', border: '1px solid #e5e7eb', borderRadius: '10px', padding: '20px', marginBottom: '14px' }
const label: React.CSSProperties = { display: 'block', fontSize: '12px', fontWeight: 600, color: '#374151', marginBottom: '4px' }
const help: React.CSSProperties = { fontSize: '12px', color: '#6b7280', marginTop: '4px' }
const input: React.CSSProperties = { width: '100%', padding: '8px 11px', border: '1px solid #d1d5db', borderRadius: '6px', fontSize: '13px', boxSizing: 'border-box' }
const primaryBtn: React.CSSProperties = { padding: '9px 18px', background: '#2563eb', color: '#fff', border: 'none', borderRadius: '7px', fontSize: '14px', fontWeight: 600, cursor: 'pointer' }
const ghostBtn: React.CSSProperties = { padding: '9px 16px', background: '#fff', color: '#374151', border: '1px solid #d1d5db', borderRadius: '7px', fontSize: '14px', cursor: 'pointer' }

type Step = 'pick' | 'configure' | 'done'

export default function OnboardingWizard() {
  const navigate = useNavigate()
  const currentTenant = useAuthStore((s) => s.currentTenant)

  const [step, setStep] = useState<Step>('pick')
  const [template, setTemplate] = useState<PipelineTemplate | null>(null)
  const [name, setName] = useState('')
  // Per-node working copy of the template config the user edits.
  const [configs, setConfigs] = useState<Record<string, Record<string, unknown>>>({})

  const [deploying, setDeploying] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [deployedId, setDeployedId] = useState<string | null>(null)
  const [webhookUrl, setWebhookUrl] = useState<string>('')
  const [sampleResult, setSampleResult] = useState<string | null>(null)

  const finish = () => {
    markOnboarded(currentTenant?.id)
    navigate('/')
  }

  const choose = (t: PipelineTemplate) => {
    // Deep-clone the template configs so edits don't mutate the catalog.
    const cloned: Record<string, Record<string, unknown>> = {}
    for (const n of t.nodes) cloned[n.id] = JSON.parse(JSON.stringify(n.config))
    setTemplate(t)
    setConfigs(cloned)
    setName(t.name)
    setError(null)
    setStep('configure')
  }

  const setFieldValue = (f: TemplateField, value: string) => {
    setConfigs((prev) => {
      const next = JSON.parse(JSON.stringify(prev)) as Record<string, Record<string, unknown>>
      getObj(next[f.nodeId], f.objectPath)[f.key] = value
      return next
    })
  }

  const patchSecret = (f: TemplateField, patch: Record<string, unknown>) => {
    setConfigs((prev) => {
      const next = JSON.parse(JSON.stringify(prev)) as Record<string, Record<string, unknown>>
      Object.assign(getObj(next[f.nodeId], f.objectPath), patch)
      return next
    })
  }

  // Every template field is required before deploy. A secret field is satisfied
  // by either freshly-typed plaintext (`key`) or an already-bound secret
  // (`key_secret_id`); a text field by a non-empty string.
  const missing = useMemo(() => {
    if (!template) return []
    return template.fields.filter((f) => {
      const obj = readObj(configs[f.nodeId] || {}, f.objectPath)
      const v = obj[f.key]
      const filled = typeof v === 'string' && v.trim() !== ''
      if (f.secret) {
        const bound = obj[`${f.key}_secret_id`]
        return !filled && !(typeof bound === 'string' && bound !== '')
      }
      return !filled
    })
  }, [template, configs])

  async function deploy() {
    if (!template) return
    setDeploying(true)
    setError(null)
    try {
      const builtNodes = await Promise.all(
        template.nodes.map(async (n) => ({
          id: n.id,
          type: n.type,
          config: (await materializeSecrets(configs[n.id] || {}, n.id)) as Record<string, unknown>,
          enabled: true,
        }))
      )
      const payload = {
        name: name.trim() || template.name,
        description: `Created from the "${template.name}" template`,
        nodes: builtNodes,
        edges: template.edges.map((e, i) => ({ id: `edge-${i}`, source: e.source, target: e.target, order: i })),
      }
      const resp = await apiClient.post('/api/v1/connections', payload)
      const id = resp.data?.data?.id
      if (!id) throw new Error('No connection ID returned from server')
      try {
        await apiClient.post(`/api/v1/connections/${id}/start`)
      } catch {
        /* best-effort auto-start; connection still created */
      }
      setDeployedId(id)
      if (template.webhookSource) setWebhookUrl(`${config.webhookIngressUrl}/webhook/${id}`)
      markOnboarded(currentTenant?.id)
      setStep('done')
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to deploy the pipeline')
    } finally {
      setDeploying(false)
    }
  }

  async function sendSample() {
    if (!webhookUrl || !template) return
    setSampleResult('sending…')
    try {
      const r = await fetch(webhookUrl, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(template.samplePayload ?? {}),
      })
      setSampleResult(r.ok ? `✓ Delivered (HTTP ${r.status})` : `Webhook returned HTTP ${r.status}`)
    } catch {
      setSampleResult('Could not reach the webhook')
    }
  }

  // --- render --------------------------------------------------------------

  return (
    <div style={page}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: '8px' }}>
        <h1 style={{ fontSize: '26px', fontWeight: 700, margin: 0 }}>Get started</h1>
        <button onClick={finish} style={{ ...ghostBtn, padding: '6px 12px', fontSize: '13px' }}>Skip for now</button>
      </div>
      <p style={{ fontSize: '14px', color: '#6b7280', marginTop: 0, marginBottom: '24px' }}>
        Pick a template and deploy your first pipeline in a couple of minutes.
      </p>

      {error && (
        <div style={{ padding: '10px 12px', background: '#fef2f2', color: '#991b1b', fontSize: '13px', borderRadius: '8px', marginBottom: '14px' }}>
          {error}
        </div>
      )}

      {step === 'pick' && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(240px, 1fr))', gap: '14px' }}>
          {TEMPLATES.map((t) => (
            <button
              key={t.id}
              onClick={() => choose(t)}
              style={{ ...card, textAlign: 'left', cursor: 'pointer', marginBottom: 0, display: 'flex', flexDirection: 'column', gap: '6px' }}
            >
              <div style={{ fontSize: '26px' }}>{t.icon}</div>
              <div style={{ fontSize: '15px', fontWeight: 600 }}>{t.name}</div>
              <div style={{ fontSize: '13px', color: '#6b7280', lineHeight: 1.4 }}>{t.summary}</div>
              <div style={{ marginTop: '6px', fontSize: '11px', fontWeight: 600, color: '#2563eb' }}>
                {t.sourceLabel} → {t.destLabel}
              </div>
            </button>
          ))}
        </div>
      )}

      {step === 'configure' && template && (
        <>
          <div style={card}>
            <div style={{ fontSize: '15px', fontWeight: 600, marginBottom: '2px' }}>
              {template.icon} {template.name}
            </div>
            <div style={{ fontSize: '13px', color: '#6b7280' }}>{template.summary}</div>
          </div>

          <div style={card}>
            <label style={label}>Pipeline name</label>
            <input style={input} value={name} onChange={(e) => setName(e.target.value)} />
          </div>

          {template.fields.map((f) => {
            const obj = readObj(configs[f.nodeId] || {}, f.objectPath)
            return (
              <div style={card} key={`${f.nodeId}.${f.objectPath}.${f.key}`}>
                {f.secret ? (
                  <SecretInput
                    label={f.label}
                    field={f.key}
                    config={obj}
                    placeholder={f.placeholder}
                    onChange={(patch) => patchSecret(f, patch)}
                  />
                ) : (
                  <>
                    <label style={label}>{f.label}</label>
                    <input
                      style={input}
                      value={(obj[f.key] as string) ?? ''}
                      placeholder={f.placeholder}
                      onChange={(e) => setFieldValue(f, e.target.value)}
                    />
                  </>
                )}
                <div style={help}>
                  {f.help}
                  {f.link && (
                    <>
                      {' '}
                      <a href={f.link.url} target="_blank" rel="noopener noreferrer" style={{ color: '#2563eb' }}>
                        {f.link.text} ↗
                      </a>
                    </>
                  )}
                </div>
              </div>
            )
          })}

          <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: '6px' }}>
            <button onClick={() => setStep('pick')} style={ghostBtn}>← Back</button>
            <button onClick={deploy} disabled={deploying || missing.length > 0} style={{ ...primaryBtn, opacity: deploying || missing.length > 0 ? 0.6 : 1 }}>
              {deploying ? 'Deploying…' : 'Deploy pipeline'}
            </button>
          </div>
          {missing.length > 0 && (
            <div style={{ ...help, textAlign: 'right' }}>Fill in: {missing.map((m) => m.label).join(', ')}</div>
          )}
        </>
      )}

      {step === 'done' && template && (
        <div style={card}>
          <div style={{ fontSize: '18px', fontWeight: 700, marginBottom: '4px' }}>🎉 {name || template.name} is live</div>
          <p style={{ fontSize: '13px', color: '#6b7280', marginTop: 0 }}>
            Your pipeline is deployed and running.
          </p>

          {template.webhookSource && webhookUrl && (
            <div style={{ marginTop: '10px' }}>
              <label style={label}>Your webhook URL</label>
              <code style={{ display: 'block', background: '#f3f4f6', padding: '9px 11px', borderRadius: '6px', fontSize: '12px', wordBreak: 'break-all' }}>
                {webhookUrl}
              </code>
              <p style={help}>POST events here and they flow through your pipeline. Try it now:</p>
              <button onClick={sendSample} style={{ ...ghostBtn, marginTop: '4px' }}>Send a sample event</button>
              {sampleResult && (
                <span style={{ marginLeft: '10px', fontSize: '13px', color: sampleResult.startsWith('✓') ? '#059669' : '#b45309' }}>
                  {sampleResult}
                </span>
              )}
            </div>
          )}

          <div style={{ display: 'flex', gap: '10px', marginTop: '18px' }}>
            <button onClick={finish} style={primaryBtn}>Go to dashboard</button>
            {deployedId && (
              <button onClick={() => navigate(`/connections/${deployedId}`)} style={ghostBtn}>
                View connection
              </button>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
