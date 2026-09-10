/**
 * materializeSecrets is the single point where a credential the user typed
 * either becomes an encrypted tenant secret or gets persisted in plain text.
 * Both the visual builder and the onboarding wizard deploy through it.
 *
 * It had 6.7% statement coverage. Today's onboarding tests mocked it, so the
 * only thing standing behind the platform's "never persist plaintext" promise
 * on the client side was that nobody had broken it yet.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'

const createSecret = vi.fn()
vi.mock('../services/secretService', () => ({
  createSecret: (name: string, value: string) => createSecret(name, value),
}))

import { materializeSecrets, SECRET_FIELDS } from './secrets'

beforeEach(() => {
  createSecret.mockReset()
  let n = 0
  createSecret.mockImplementation(async () => ({ id: `sec-${++n}` }))
})

describe('credentials never survive as plaintext', () => {
  it('replaces a secret field with a _secret_id reference', async () => {
    const out = (await materializeSecrets(
      { type: 'sitoo', sitoo: { api_id: 'public-id', api_password: 'hunter2' } },
      'node-1'
    )) as Record<string, Record<string, unknown>>

    expect(out.sitoo.api_password_secret_id).toBe('sec-1')
    expect(out.sitoo).not.toHaveProperty('api_password')
    // Non-credential fields are untouched.
    expect(out.sitoo.api_id).toBe('public-id')
    // And the plaintext is nowhere in the serialized result.
    expect(JSON.stringify(out)).not.toContain('hunter2')
  })

  it('handles every field in SECRET_FIELDS', async () => {
    // A field added to the set but not handled here would be a credential the
    // platform believes it protects and does not.
    const config: Record<string, string> = {}
    for (const f of SECRET_FIELDS) config[f] = `plaintext-${f}`

    const out = (await materializeSecrets(config, 'n')) as Record<string, unknown>
    const serialized = JSON.stringify(out)

    for (const f of SECRET_FIELDS) {
      expect(out, `${f} was not converted`).toHaveProperty(`${f}_secret_id`)
      expect(out, `${f} plaintext survived`).not.toHaveProperty(f)
      expect(serialized).not.toContain(`plaintext-${f}`)
    }
  })

  it('walks nested objects and arrays', async () => {
    const out = (await materializeSecrets(
      { nodes: [{ config: { deep: { password: 'p1' } } }, { config: { token: 't1' } }] },
      'n'
    )) as { nodes: { config: Record<string, Record<string, unknown>> }[] }

    expect(out.nodes[0].config.deep.password_secret_id).toBeDefined()
    expect(out.nodes[1].config.token_secret_id).toBeDefined()
    expect(JSON.stringify(out)).not.toContain('p1')
    expect(JSON.stringify(out)).not.toContain('t1')
  })
})

describe('what it deliberately leaves alone', () => {
  it('does not mint a secret for an empty string', async () => {
    // An untouched field. Minting here would fill the vault with empty secrets
    // every time someone redeployed.
    const out = (await materializeSecrets({ password: '' }, 'n')) as Record<string, unknown>
    expect(createSecret).not.toHaveBeenCalled()
    expect(out.password).toBe('')
  })

  it('does not mint a secret for a non-string value', async () => {
    const out = (await materializeSecrets({ password: 12345 }, 'n')) as Record<string, unknown>
    expect(createSecret).not.toHaveBeenCalled()
    expect(out.password).toBe(12345)
  })

  it('preserves an already-bound _secret_id', async () => {
    const out = (await materializeSecrets({ password_secret_id: 'sec-existing' }, 'n')) as Record<
      string,
      unknown
    >
    expect(createSecret).not.toHaveBeenCalled()
    expect(out.password_secret_id).toBe('sec-existing')
  })

  it('drops undefined keys rather than clobbering a freshly-minted id', async () => {
    // SecretInput clears a bound id by setting it to undefined while the user
    // types a replacement. If that key were copied through, iteration order
    // could overwrite the id minted from the plaintext above it — wiping BOTH,
    // which is the comment in secrets.ts made executable.
    const out = (await materializeSecrets(
      { password: 'new-one', password_secret_id: undefined },
      'n'
    )) as Record<string, unknown>

    expect(out.password_secret_id).toBe('sec-1')
    expect(out).not.toHaveProperty('password')
  })
})

describe('failure aborts the deploy', () => {
  it('propagates a createSecret error instead of persisting plaintext', async () => {
    createSecret.mockRejectedValueOnce(new Error('vault unavailable'))

    await expect(materializeSecrets({ password: 'hunter2' }, 'n')).rejects.toThrow('vault unavailable')
  })
})

describe('scalars and edge cases pass through', () => {
  it.each([
    ['a string', 'plain'],
    ['a number', 7],
    ['null', null],
    ['a boolean', true],
  ])('%s', async (_label, input) => {
    await expect(materializeSecrets(input, 'n')).resolves.toBe(input)
  })
})
