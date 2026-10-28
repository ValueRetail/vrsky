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
import { connectorKeysFor, CONVERTER_MAPPING_TYPES } from '../types/nodeConfig'
import { TEMPLATES, type PipelineTemplate } from './templates'
import sitooToBusinessCentral from './testdata/sitoo-to-business-central.json'
import businessCentralToSitoo from './testdata/business-central-to-sitoo.json'

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
      // Sources and destinations name their connector; a converter is the
      // data-converter by its node type and keeps its settings flat.
      if (n.type === 'converter') continue
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

// --- What the connectors actually read (plans/template-fields.md) ----------
// The checks above are about the catalog's shape. These are about whether a
// deployed template does anything: on 2026-10-22 "API poll → File" wrote
// api.url / api.interval (the consumer reads base_url / endpoints /
// poll_interval_seconds) and "CSV → Database" wrote database.connection_string
// (the producer reads host/port/user/…), so one polled nothing and the other
// was refused at start. Each check names the key so the fix is obvious.

// The settings object a node's keys live in: config[block], or the config
// itself for a converter.
function settings(n: PipelineTemplate['nodes'][number], block: string): Record<string, unknown> {
  if (!block) return n.config
  const b = n.config[block]
  return b && typeof b === 'object' ? (b as Record<string, unknown>) : {}
}

describe('every node uses a connector whose keys are known', () => {
  each((t) => {
    for (const n of t.nodes) {
      expect(
        connectorKeysFor(n.type, n.config),
        `${t.id}: node ${n.id} (${n.type}/${String(n.config.type)}) is not in CONNECTOR_KEYS; add it there first`
      ).toBeDefined()
    }
  })
})

describe('no template writes a key its connector ignores', () => {
  each((t) => {
    for (const n of t.nodes) {
      const ck = connectorKeysFor(n.type, n.config)
      if (!ck) continue
      if (ck.block) {
        for (const k of Object.keys(n.config)) {
          expect(['type', ck.block], `${t.id}: node ${n.id} has a stray top-level key "${k}"`).toContain(k)
        }
      }
      for (const k of Object.keys(settings(n, ck.block))) {
        expect(ck.keys, `${t.id}: node ${n.id} sets "${k}", which ${n.type}/${String(n.config.type ?? n.type)} never reads`).toContain(k)
      }
    }
  })
})

describe('every key the connector needs is there or asked for', () => {
  each((t) => {
    for (const n of t.nodes) {
      const ck = connectorKeysFor(n.type, n.config)
      if (!ck) continue
      const s = settings(n, ck.block)
      for (const k of ck.required) {
        const asked = t.fields.some((f) => f.nodeId === n.id && f.key === k && f.objectPath === ck.block)
        const pre = s[k]
        const present = Array.isArray(pre) ? pre.length > 0 : pre !== undefined && pre !== ''
        expect(present || asked, `${t.id}: node ${n.id} needs "${k}", which is neither pre-filled nor a wizard field`).toBe(true)
      }
    }
  })
})

describe('numbers stay numbers', () => {
  // The Go structs read these as ints; a string fails to unmarshal.
  each((t) => {
    for (const n of t.nodes) {
      const ck = connectorKeysFor(n.type, n.config)
      if (!ck) continue
      const s = settings(n, ck.block)
      for (const k of ck.numeric) {
        if (s[k] !== undefined) {
          expect(typeof s[k], `${t.id}: node ${n.id} pre-fills "${k}" as ${typeof s[k]}`).toBe('number')
        }
      }
      for (const f of t.fields.filter((f) => f.nodeId === n.id)) {
        expect(
          Boolean(f.numeric),
          `${t.id}: wizard field "${f.key}" must be ${ck.numeric.includes(f.key) ? '' : 'not '}marked numeric`
        ).toBe(ck.numeric.includes(f.key))
      }
    }
  })
})

describe('every wizard field lands on a key the connector reads', () => {
  each((t) => {
    for (const f of t.fields) {
      const n = t.nodes.find((n) => n.id === f.nodeId)
      if (!n) continue // reported by "every field points at a node that exists"
      const ck = connectorKeysFor(n.type, n.config)
      if (!ck) continue
      // Path segments below the settings block: api.endpoints.0 → [endpoints, 0]
      // for an api source; mappings.0 → [mappings, 0] for a converter, whose
      // settings are the config root.
      const segs = f.objectPath ? f.objectPath.split('.') : []
      if (ck.block) {
        expect(segs[0], `${t.id}: field "${f.key}" writes into "${f.objectPath}", outside the ${ck.block} settings`).toBe(ck.block)
        segs.shift()
      }
      if (segs.length === 0) {
        expect(ck.keys, `${t.id}: field "${f.key}" is a key the connector never reads`).toContain(f.key)
      } else {
        // A nested path must start at a key the connector reads.
        expect(ck.keys, `${t.id}: field "${f.key}" is nested under an unknown key`).toContain(segs[0])
      }
    }
  })
})

// --- Converter mappings (#435) ----------------------------------------------
// The retail templates carry a field mapping. The converter skips a mapping
// type it does not know without a word, and a wizard value written anywhere
// but a static mapping's value is ignored; both deploy a pipeline that drops a
// field the target needs.

type Mapping = { type?: string; value?: unknown; mappings?: Mapping[] }

function allMappings(ms: Mapping[] | undefined): Mapping[] {
  return (ms ?? []).flatMap((m) => [m, ...allMappings(m.mappings)])
}

describe('converter mappings use types the converter implements', () => {
  each((t) => {
    for (const n of t.nodes.filter((n) => n.type === 'converter')) {
      for (const m of allMappings(n.config.mappings as Mapping[] | undefined)) {
        expect(CONVERTER_MAPPING_TYPES, `${t.id}: node ${n.id} uses mapping type "${m.type}"`).toContain(m.type)
        if (m.type === 'each') {
          expect(m.mappings?.length, `${t.id}: an "each" mapping in node ${n.id} has no nested mappings`).toBeGreaterThan(0)
        }
      }
    }
  })
})

describe('a wizard value inside a converter mapping is a static value', () => {
  each((t) => {
    for (const f of t.fields) {
      const n = t.nodes.find((n) => n.id === f.nodeId)
      if (n?.type !== 'converter' || !f.objectPath.startsWith('mappings.')) continue
      const m = at(n.config, f.objectPath) as Mapping | undefined
      expect(m?.type, `${t.id}: field "${f.label}" writes into a ${m?.type} mapping, which ignores "value"`).toBe('static')
      expect(f.key, `${t.id}: field "${f.label}" sets "${f.key}" on a static mapping`).toBe('value')
    }
  })
})

describe('the retail templates deploy the mapping the Go tests check', () => {
  // src/cmd/data-converter/template_mapping_test.go runs the real converter
  // with these files on Sitoo and BC fixtures. A mapping changed here but not
  // there would ship untested.
  const goTestdata: Record<string, unknown> = {
    'sitoo-to-business-central': sitooToBusinessCentral,
    'business-central-to-sitoo': businessCentralToSitoo,
  }
  for (const [id, want] of Object.entries(goTestdata)) {
    it(id, () => {
      const t = TEMPLATES.find((t) => t.id === id)
      expect(t, `template ${id} is not in the catalog`).toBeDefined()
      const conv = t!.nodes.filter((n) => n.type === 'converter')
      expect(conv).toHaveLength(1)
      expect(conv[0].config).toEqual(want)
    })
  }
})

describe('the retail templates', () => {
  it('both directions are in the gallery and say they are beta', () => {
    for (const id of ['sitoo-to-business-central', 'business-central-to-sitoo']) {
      const t = TEMPLATES.find((t) => t.id === id)
      expect(t, id).toBeDefined()
      expect(t!.summary).toMatch(/^Beta: verify the mapping/)
    }
  })

  it('a BC destination skips sales invoices it already has', () => {
    // The Sitoo source re-reads every receipt on each poll and split_records
    // republishes a whole page on redelivery: without this BC gets duplicates.
    const dst = TEMPLATES.find((t) => t.id === 'sitoo-to-business-central')!.nodes.find((n) => n.id === 'dst')!
    expect((dst.config.business_central as Record<string, unknown>).dedupe_fields).toEqual(['externalDocumentNumber'])
  })
})
