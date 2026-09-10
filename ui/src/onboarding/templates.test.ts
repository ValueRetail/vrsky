/**
 * Invariants for the onboarding template catalog.
 *
 * The wizard is the path a non-developer takes to their first pipeline, and
 * every failure below is silent — the wizard renders, the user fills the form,
 * deploy returns 200, and the resulting pipeline is wrong. None of it would
 * throw where anyone is looking.
 */

import { describe, it, expect } from 'vitest'
import { SECRET_FIELDS } from '../utils/secrets'
import { TEMPLATES, type PipelineTemplate } from './templates'

// Walks a dot-path without creating anything, mirroring the wizard's readObj.
function at(root: Record<string, unknown>, path: string): unknown {
  if (!path) return root
  let cur: unknown = root
  for (const part of path.split('.')) {
    if (!cur || typeof cur !== 'object') return undefined
    cur = (cur as Record<string, unknown>)[part]
  }
  return cur
}

const each = (fn: (t: PipelineTemplate) => void) =>
  TEMPLATES.forEach((t) => it(t.id, () => fn(t)))

describe('the catalog is not empty', () => {
  it('ships templates, and each has a unique id', () => {
    expect(TEMPLATES.length).toBeGreaterThan(0)
    const ids = TEMPLATES.map((t) => t.id)
    expect(new Set(ids).size).toBe(ids.length)
  })
})

describe('every field points at a node that exists', () => {
  // A field whose nodeId is a typo writes into `configs[undefined]`. The input
  // renders, accepts what the user types, and the value goes nowhere — so the
  // pipeline deploys with an empty URL or no credential at all.
  each((t) => {
    const nodeIds = new Set(t.nodes.map((n) => n.id))
    for (const f of t.fields) {
      expect(nodeIds, `field ${f.key} targets unknown node ${f.nodeId}`).toContain(f.nodeId)
    }
  })
})

describe('every edge connects nodes that exist', () => {
  each((t) => {
    const nodeIds = new Set(t.nodes.map((n) => n.id))
    for (const e of t.edges) {
      expect(nodeIds, `edge source ${e.source} is not a node`).toContain(e.source)
      expect(nodeIds, `edge target ${e.target} is not a node`).toContain(e.target)
    }
  })
})

describe('every template is a deployable pipeline', () => {
  // The backend rejects a connection without exactly this shape, but the user
  // only finds out at the end, after filling in credentials.
  each((t) => {
    expect(t.nodes.filter((n) => n.type === 'consumer').length).toBeGreaterThanOrEqual(1)
    expect(t.nodes.filter((n) => n.type === 'producer').length).toBeGreaterThanOrEqual(1)
    expect(t.edges.length).toBeGreaterThanOrEqual(1)
    for (const n of t.nodes) {
      expect(typeof n.config.type, `node ${n.id} has no connector type`).toBe('string')
    }
  })
})

describe('secret fields are actually encrypted at deploy', () => {
  // THE IMPORTANT ONE. materializeSecrets only mints a tenant secret for keys
  // in SECRET_FIELDS. A field marked `secret: true` whose key is not in that
  // set renders as a masked input — so it looks handled — and is then persisted
  // as PLAINTEXT in the connection config. The mask is the whole lie.
  each((t) => {
    for (const f of t.fields.filter((f) => f.secret)) {
      expect(
        SECRET_FIELDS.has(f.key),
        `${t.id}: field "${f.key}" is marked secret but is not in SECRET_FIELDS, so ` +
          `materializeSecrets will not encrypt it and it will be stored in plaintext`
      ).toBe(true)
    }
  })
})

describe('no template ships a hardcoded credential', () => {
  // A non-secret field may absolutely be pre-filled — "/data/output" and a 60s
  // poll interval are sensible defaults the user can override, and the wizard
  // treats them as already satisfied. A SECRET field is the opposite: a value
  // baked into the catalog is a credential committed to the repo, and every
  // tenant using that template would share it.
  each((t) => {
    for (const f of t.fields.filter((f) => f.secret)) {
      const obj = at(t.nodes.find((n) => n.id === f.nodeId)!.config as Record<string, unknown>, f.objectPath)
      const pre = obj && typeof obj === 'object' ? (obj as Record<string, unknown>)[f.key] : undefined
      expect(
        pre === undefined || pre === '',
        `${t.id}: secret field "${f.key}" ships with the value ${JSON.stringify(pre)} baked in`
      ).toBe(true)
    }
  })
})

describe('webhook templates can demonstrate themselves', () => {
  // The "Send a sample event" button POSTs samplePayload. Without one the done
  // screen offers a button that proves nothing.
  each((t) => {
    if (!t.webhookSource) return
    expect(t.samplePayload, `${t.id} is a webhook template with no samplePayload`).toBeDefined()
  })
})

describe('gallery cards are presentable', () => {
  each((t) => {
    for (const k of ['name', 'summary', 'icon', 'sourceLabel', 'destLabel'] as const) {
      expect(typeof t[k], `${t.id}.${k}`).toBe('string')
      expect((t[k] as string).trim(), `${t.id}.${k} is empty`).not.toBe('')
    }
    for (const f of t.fields) {
      expect(f.label.trim(), `${t.id}: a field has no label`).not.toBe('')
      expect(f.help.trim(), `${t.id}: field "${f.key}" has no help text`).not.toBe('')
    }
  })
})
