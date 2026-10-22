/**
 * The deploy sequence keeps the connection id, and never replaces it quietly.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { deployPipeline, create, ConnectionGone, deployIdentity, NEW_PIPELINE_DESCRIPTION } from './deployPipeline'
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

describe('the builder names its deploy through deployIdentity', () => {
  // The page is a large canvas component; this pins the one wiring that
  // matters here: the payload's name comes from deployIdentity and nowhere
  // else, so the old `Pipeline <time>` literal cannot come back on updates.
  it('buildConnectionPayload spreads deployIdentity and builds no name itself', () => {
    const src = builderSource
    const start = src.indexOf('const buildConnectionPayload')
    const body = src.slice(start, src.indexOf('\n  }\n', start))
    expect(start, 'buildConnectionPayload not found').toBeGreaterThan(0)
    expect(body).toContain('...deployIdentity(')
    expect(body).not.toMatch(/name:\s*`Pipeline/)
    expect(body).not.toMatch(/description:\s*'/)
  })
})
