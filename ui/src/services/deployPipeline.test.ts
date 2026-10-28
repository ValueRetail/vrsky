/**
 * The deploy sequence keeps the connection id, and never replaces it quietly.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import {
  deployPipeline, create, ConnectionGone, deployIdentity, NEW_PIPELINE_DESCRIPTION, deployPlan, deployCanvas,
} from './deployPipeline'
import builderSource from '../pages/PipelineBuilder.tsx?raw'

const post = vi.fn()
const put = vi.fn()
const client = { post, put } as never

const httpErr = (status: number) => Object.assign(new Error(`HTTP ${status}`), { response: { status } })
const vrskyErr = (status: number) => Object.assign(new Error(`HTTP Error ${status}`), { details: { status } })

beforeEach(() => {
  post.mockReset()
  put.mockReset()
})

describe('deployPipeline', () => {
  it('first deploy creates', async () => {
    post.mockResolvedValue({ data: { data: { id: 'new-1' } } })
    await expect(deployPipeline(client, { name: 'p' }, undefined)).resolves.toEqual({ connectionId: 'new-1', created: true })
    expect(post).toHaveBeenCalledWith('/api/v1/connections', { name: 'p' })
    expect(put).not.toHaveBeenCalled()
  })

  it('redeploy keeps the id: stop, update in place, no create', async () => {
    post.mockResolvedValue({})
    put.mockResolvedValue({})
    await expect(deployPipeline(client, { name: 'p' }, 'old-1')).resolves.toEqual({ connectionId: 'old-1', created: false })
    expect(post).toHaveBeenCalledWith('/api/v1/connections/old-1/stop')
    expect(put).toHaveBeenCalledWith('/api/v1/connections/old-1', { name: 'p' })
    expect(post).not.toHaveBeenCalledWith('/api/v1/connections', expect.anything())
  })

  it('an already-stopped connection is still updated in place', async () => {
    post.mockRejectedValue(httpErr(400))
    put.mockResolvedValue({})
    await expect(deployPipeline(client, {}, 'old-1')).resolves.toEqual({ connectionId: 'old-1', created: false })
  })

  it.each([
    ['stop', () => { post.mockRejectedValue(httpErr(404)) }],
    ['update', () => { post.mockResolvedValue({}); put.mockRejectedValue(vrskyErr(404)) }],
  ])('a deleted connection (404 on %s) is reported, not replaced', async (_step, arrange) => {
    arrange()
    const err = await deployPipeline(client, {}, 'old-1').catch((e) => e)
    expect(err).toBeInstanceOf(ConnectionGone)
    expect((err as ConnectionGone).connectionId).toBe('old-1')
    expect(post).not.toHaveBeenCalledWith('/api/v1/connections', expect.anything())
  })

  it('any other update failure is an error and never creates', async () => {
    post.mockResolvedValue({})
    put.mockRejectedValue(vrskyErr(400))
    await expect(deployPipeline(client, {}, 'old-1')).rejects.toThrow('HTTP Error 400')
    expect(post).not.toHaveBeenCalledWith('/api/v1/connections', expect.anything())
  })

  it('create insists on an id coming back', async () => {
    post.mockResolvedValue({ data: {} })
    await expect(create(client, {})).rejects.toThrow('No connection ID')
  })
})

// plans/builder-deploy-names.md: every deploy used to rename the pipeline to
// `Pipeline <time>` and reset its description, the update included.
describe('deployIdentity', () => {
  const now = new Date('2026-10-22T12:00:00Z')
  const stamp = `Pipeline ${now.toLocaleTimeString()}`

  it.each([
    ['an update of an edited connection keeps the server name and description',
      { name: 'Edit: BC → Bifrost', deployedConnectionId: 'c1', deployedName: 'BC → Bifrost' }, {}],
    ['an update after renaming the tab sends the new name, still no description',
      { name: 'Edit: Bifrost items', deployedConnectionId: 'c1', deployedName: 'BC → Bifrost' }, { name: 'Bifrost items' }],
    ['an update from an untitled canvas keeps the server name',
      { name: 'Tenant 2', deployedConnectionId: 'c1', deployedName: 'Pipeline 2:26:01 PM' }, {}],
    ['an update whose server name is unknown sends a titled tab name',
      { name: 'Webhook in', deployedConnectionId: 'c1' }, { name: 'Webhook in' }],
    ['a create from an edit tab drops the "Edit: " prefix',
      { name: 'Edit: CSV → Database' }, { name: 'CSV → Database', description: NEW_PIPELINE_DESCRIPTION }],
    ['a create from a named tab uses the tab name',
      { name: 'Orders to ERP' }, { name: 'Orders to ERP', description: NEW_PIPELINE_DESCRIPTION }],
    ['a create from an untitled canvas falls back to Pipeline <time>',
      { name: 'Tenant 1' }, { name: stamp, description: NEW_PIPELINE_DESCRIPTION }],
    ['a create from an empty name falls back too',
      { name: '   ' }, { name: stamp, description: NEW_PIPELINE_DESCRIPTION }],
  ])('%s', (_, canvas, want) => {
    expect(deployIdentity(canvas, now)).toEqual(want)
  })

  it('an update never carries a description', () => {
    for (const name of ['Edit: x', 'y', 'Tenant 3', '']) {
      expect(deployIdentity({ name, deployedConnectionId: 'c1', deployedName: 'old' }, now)).not.toHaveProperty('description')
    }
  })
})

// #355: the canvas's connection was deleted on the server. The first deploy
// finds it gone and the page asks; "Deploy as new" must create exactly one
// connection, named like a create, and link the canvas to it, without going
// back to the deleted id.
describe('deploy as new after the connection is gone', () => {
  const now = new Date('2026-10-27T09:00:00Z')
  const canvas = { name: 'Edit: Orders to ERP', deployedConnectionId: 'gone-1', deployedName: 'Orders to ERP' }

  it('creates one connection named after the tab and links the canvas to it', async () => {
    post.mockImplementation((url: string) => {
      if (url === '/api/v1/connections/gone-1/stop') return Promise.reject(httpErr(404))
      if (url === '/api/v1/connections') return Promise.resolve({ data: { data: { id: 'new-1' } } })
      return Promise.resolve({ data: {} })
    })
    put.mockImplementation((url: string) =>
      url === '/api/v1/connections/gone-1' ? Promise.reject(httpErr(404)) : Promise.resolve({ data: {} }))
    const link = vi.fn()

    const first = deployPlan(canvas, false, now)
    const err = await deployCanvas(client, { ...first.identity }, first.previousConnectionId, link).catch((e) => e)
    expect(err).toBeInstanceOf(ConnectionGone)
    expect(link).toHaveBeenCalledTimes(1)
    expect(link).toHaveBeenCalledWith(null)

    const postsBefore = post.mock.calls.length
    const putsBefore = put.mock.calls.length
    link.mockClear()

    const again = deployPlan(canvas, true, now)
    const payload = { ...again.identity, nodes: [], edges: [] }
    await expect(deployCanvas(client, payload, again.previousConnectionId, link))
      .resolves.toEqual({ connectionId: 'new-1', created: true })

    const creates = post.mock.calls.filter(([url]) => url === '/api/v1/connections')
    expect(creates).toHaveLength(1)
    const toGone = [...post.mock.calls.slice(postsBefore), ...put.mock.calls.slice(putsBefore)]
      .filter(([url]) => String(url).startsWith('/api/v1/connections/gone-1'))
    expect(toGone).toEqual([])
    expect(creates[0][1]).toMatchObject({ name: 'Orders to ERP', description: NEW_PIPELINE_DESCRIPTION })
    expect(link).toHaveBeenCalledTimes(1)
    expect(link).toHaveBeenCalledWith('new-1', 'Orders to ERP')
  })
})

describe('deployPlan', () => {
  const now = new Date('2026-10-27T09:00:00Z')
  const linked = { name: 'Edit: Orders to ERP', deployedConnectionId: 'c1', deployedName: 'Orders to ERP' }
  const renamed = { name: 'Edit: Orders v2', deployedConnectionId: 'c1', deployedName: 'Orders to ERP' }
  const unlinked = { name: 'Edit: Orders to ERP' }
  const asCreate = { name: 'Orders to ERP', description: NEW_PIPELINE_DESCRIPTION }

  it.each([
    ['a linked canvas updates its connection, keeping the server name', linked, false,
      { previousConnectionId: 'c1', identity: {} }],
    ['a linked, renamed canvas updates and sends only the new name', renamed, false,
      { previousConnectionId: 'c1', identity: { name: 'Orders v2' } }],
    ['"deploy as new" on a linked canvas creates, named like a create', linked, true,
      { previousConnectionId: undefined, identity: asCreate }],
    ['"deploy as new" on a renamed canvas creates under the tab name', renamed, true,
      { previousConnectionId: undefined, identity: { name: 'Orders v2', description: NEW_PIPELINE_DESCRIPTION } }],
    ['an unlinked canvas creates', unlinked, false, { previousConnectionId: undefined, identity: asCreate }],
    ['an unlinked canvas creates with asNew too', unlinked, true, { previousConnectionId: undefined, identity: asCreate }],
  ])('%s', (_, canvas, asNew, want) => {
    expect(deployPlan(canvas, asNew, now)).toEqual(want)
  })

  it('without asNew the identity is deployIdentity of the canvas', () => {
    const canvases = [linked, renamed, unlinked, { name: 'Tenant 1', deployedConnectionId: 'c1' }, { name: 'Tenant 1' }]
    for (const canvas of canvases) {
      expect(deployPlan(canvas, false, now).identity).toEqual(deployIdentity(canvas, now))
    }
  })
})

describe('deployCanvas keeps the canvas link in step', () => {
  it('a create links the new id with the name sent', async () => {
    post.mockResolvedValue({ data: { data: { id: 'new-1' } } })
    const link = vi.fn()
    await deployCanvas(client, { name: 'Orders' }, undefined, link)
    expect(link).toHaveBeenCalledTimes(1)
    expect(link).toHaveBeenCalledWith('new-1', 'Orders')
  })

  it('an update that sent a name records it', async () => {
    post.mockResolvedValue({})
    put.mockResolvedValue({})
    const link = vi.fn()
    await expect(deployCanvas(client, { name: 'Renamed' }, 'c1', link))
      .resolves.toEqual({ connectionId: 'c1', created: false })
    expect(link).toHaveBeenCalledTimes(1)
    expect(link).toHaveBeenCalledWith('c1', 'Renamed')
  })

  it('an update that sent no name leaves the link alone', async () => {
    post.mockResolvedValue({})
    put.mockResolvedValue({})
    const link = vi.fn()
    await expect(deployCanvas(client, {}, 'c1', link)).resolves.toEqual({ connectionId: 'c1', created: false })
    expect(link).not.toHaveBeenCalled()
  })

  it('a gone connection unlinks the canvas and is rethrown', async () => {
    post.mockRejectedValue(httpErr(404))
    const link = vi.fn()
    const err = await deployCanvas(client, {}, 'c1', link).catch((e) => e)
    expect(err).toBeInstanceOf(ConnectionGone)
    expect(link).toHaveBeenCalledTimes(1)
    expect(link).toHaveBeenCalledWith(null)
  })

  it('any other failure is rethrown and leaves the link alone', async () => {
    post.mockResolvedValue({})
    put.mockRejectedValue(vrskyErr(400))
    const link = vi.fn()
    await expect(deployCanvas(client, { name: 'x' }, 'c1', link)).rejects.toThrow('HTTP Error 400')
    expect(link).not.toHaveBeenCalled()
  })
})

describe('the builder deploys through deployPlan and deployCanvas', () => {
  // The page is a large canvas component that the coverage gate keeps out of
  // the suite; these pin its few lines of deploy wiring instead.
  const src = builderSource
  const bodyOf = (marker: string) => {
    const start = src.indexOf(marker)
    expect(start, `${marker} not found`).toBeGreaterThan(0)
    return src.slice(start, src.indexOf('\n  }\n', start))
  }

  // The payload's name and description come from the plan and nowhere else,
  // so the old `Pipeline <time>` literal cannot come back on updates.
  it('buildConnectionPayload spreads the plan identity and builds no name itself', () => {
    const body = bodyOf('const buildConnectionPayload')
    expect(body).toContain('...identity')
    expect(body).not.toMatch(/\bname:/)
    expect(body).not.toMatch(/\bdescription:/)
  })

  it('deployPipeline takes its identity and target from deployPlan', () => {
    const body = bodyOf('const deployPipeline = async')
    expect(body).toMatch(/deployPlan\(/)
    expect(body).toContain('buildConnectionPayload(plan.identity)')
    expect(body).toMatch(/deployCanvas\(apiClient, payload, plan\.previousConnectionId,/)
    expect(src).not.toMatch(/deployIdentity\(/)
  })

  it('"Deploy as new" redeploys with asNew', () => {
    const body = bodyOf('const deployPipeline = async')
    const start = body.indexOf('onConfirm:')
    expect(start, 'onConfirm not found').toBeGreaterThan(0)
    const onConfirm = body.slice(start, body.indexOf('onCancel:', start))
    expect(onConfirm).toContain('deployPipeline(true)')
  })

  it('the Deploy button does not pass its click event as asNew', () => {
    expect(src).not.toMatch(/onClick=\{deployPipeline\}/)
    expect(src).toContain('onClick={() => deployPipeline()}')
  })
})
