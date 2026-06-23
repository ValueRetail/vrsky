/**
 * Onboarding template catalog tests (Phase 4B / #93).
 *
 * Guards the contract the wizard relies on: enough templates, valid node/edge
 * graphs, and every field pointing at a real connector config path. A broken
 * template would otherwise only surface at deploy time in the UI.
 */

import { describe, it, expect } from 'vitest'
import { TEMPLATES, templateById, type PipelineTemplate } from '@/onboarding/templates'
import { SECRET_FIELDS } from '@/utils/secrets'

function nodeConfig(t: PipelineTemplate, nodeId: string): Record<string, unknown> | undefined {
  return t.nodes.find((n) => n.id === nodeId)?.config
}

describe('onboarding templates', () => {
  it('ships at least 4 templates (acceptance criterion)', () => {
    expect(TEMPLATES.length).toBeGreaterThanOrEqual(4)
  })

  it('has unique ids and templateById resolves them', () => {
    const ids = TEMPLATES.map((t) => t.id)
    expect(new Set(ids).size).toBe(ids.length)
    for (const t of TEMPLATES) expect(templateById(t.id)).toBe(t)
  })

  it('every template is a valid consumer→producer graph', () => {
    for (const t of TEMPLATES) {
      const types = t.nodes.map((n) => n.type)
      expect(types).toContain('consumer')
      expect(types).toContain('producer')
      // Edges reference real nodes.
      const nodeIds = new Set(t.nodes.map((n) => n.id))
      expect(t.edges.length).toBeGreaterThan(0)
      for (const e of t.edges) {
        expect(nodeIds.has(e.source)).toBe(true)
        expect(nodeIds.has(e.target)).toBe(true)
      }
      // Every node config declares a connector "type".
      for (const n of t.nodes) expect(typeof n.config.type).toBe('string')
    }
  })

  it('every field points at a real node + object path, and secrets use SECRET_FIELDS', () => {
    for (const t of TEMPLATES) {
      for (const f of t.fields) {
        const cfg = nodeConfig(t, f.nodeId)
        expect(cfg, `${t.id}: field references missing node ${f.nodeId}`).toBeDefined()
        // objectPath must resolve to an object in the pre-filled config.
        let cur: unknown = cfg
        if (f.objectPath) {
          for (const part of f.objectPath.split('.')) {
            expect(cur && typeof cur === 'object').toBe(true)
            cur = (cur as Record<string, unknown>)[part]
          }
        }
        expect(cur && typeof cur === 'object', `${t.id}.${f.key}: objectPath "${f.objectPath}" is not an object`).toBe(true)
        expect(f.label.length).toBeGreaterThan(0)
        expect(f.help.length).toBeGreaterThan(0)
        if (f.secret) {
          expect(SECRET_FIELDS.has(f.key), `${t.id}: secret field "${f.key}" not in SECRET_FIELDS`).toBe(true)
        }
      }
    }
  })

  it('webhook-source templates carry a sample payload to send', () => {
    for (const t of TEMPLATES) {
      if (t.webhookSource) {
        expect(t.samplePayload, `${t.id}: webhook template needs a samplePayload`).toBeDefined()
      }
    }
  })
})
