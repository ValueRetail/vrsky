/**
 * The deploy sequence keeps the connection id, and never replaces it quietly.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { deployPipeline, create, ConnectionGone } from './deployPipeline'

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
