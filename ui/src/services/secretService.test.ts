/**
 * The secrets API client — 0% covered before this.
 *
 * Every function here is a thin wrapper, which is exactly why the mistakes it
 * can hide are silent: a wrong path or a wrong response unwrap returns
 * undefined rather than throwing, and materializeSecrets would then bind a
 * connection config to `undefined` as its secret id.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'

// vi.hoisted: vi.mock is lifted above every const, so a factory that closes
// over a plain top-level variable reads it before initialization.
const client = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  delete: vi.fn(),
}))
vi.mock('./api', () => ({ default: client }))

import {
  listSecrets,
  getSecret,
  createSecret,
  updateSecret,
  rotateSecret,
  deleteSecret,
} from './secretService'

const secret = { id: 'sec-1', name: 'db-password', created_at: 't0', updated_at: 't0' }

beforeEach(() => {
  vi.clearAllMocks()
})

describe('routes', () => {
  it('lists from /api/v1/secrets', async () => {
    client.get.mockResolvedValue({ data: { data: [secret] } })
    await expect(listSecrets()).resolves.toEqual([secret])
    expect(client.get).toHaveBeenCalledWith('/api/v1/secrets')
  })

  it('gets one by id', async () => {
    client.get.mockResolvedValue({ data: { data: secret } })
    await expect(getSecret('sec-1')).resolves.toEqual(secret)
    expect(client.get).toHaveBeenCalledWith('/api/v1/secrets/sec-1')
  })

  it('creates with name and value', async () => {
    client.post.mockResolvedValue({ data: { data: secret } })
    await expect(createSecret('db-password', 'hunter2')).resolves.toEqual(secret)
    expect(client.post).toHaveBeenCalledWith('/api/v1/secrets', {
      name: 'db-password',
      value: 'hunter2',
    })
  })

  it('updates by id', async () => {
    client.put.mockResolvedValue({ data: { data: secret } })
    await expect(updateSecret('sec-1', { name: 'renamed' })).resolves.toEqual(secret)
    expect(client.put).toHaveBeenCalledWith('/api/v1/secrets/sec-1', { name: 'renamed' })
  })

  it('rotates by id', async () => {
    client.post.mockResolvedValue({ data: { data: secret } })
    await expect(rotateSecret('sec-1')).resolves.toEqual(secret)
    expect(client.post).toHaveBeenCalledWith('/api/v1/secrets/sec-1/rotate')
  })

  it('deletes by id', async () => {
    client.delete.mockResolvedValue({})
    await expect(deleteSecret('sec-1')).resolves.toBeUndefined()
    expect(client.delete).toHaveBeenCalledWith('/api/v1/secrets/sec-1')
  })
})

describe('response handling', () => {
  it('returns an empty array when the list payload is absent', async () => {
    // The `?? []` matters: callers iterate the result, and undefined would
    // throw somewhere far from here.
    client.get.mockResolvedValue({ data: {} })
    await expect(listSecrets()).resolves.toEqual([])
  })

  it('propagates an API failure rather than resolving undefined', async () => {
    // createSecret failing must reach materializeSecrets, which aborts the
    // deploy. Swallowing it would bind a config to an undefined secret id.
    client.post.mockRejectedValue(new Error('403 forbidden'))
    await expect(createSecret('n', 'v')).rejects.toThrow('403 forbidden')
  })

  it('never sends a plaintext value on a read path', async () => {
    client.get.mockResolvedValue({ data: { data: secret } })
    await getSecret('sec-1')
    expect(client.get).toHaveBeenCalledWith('/api/v1/secrets/sec-1')
    expect(client.get.mock.calls[0]).toHaveLength(1) // no body
  })
})
