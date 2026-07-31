/**
 * Shared credential-materialization used at deploy time by both the visual
 * pipeline builder and the onboarding wizard (#93). Kept in one place so the
 * security-sensitive "never persist plaintext" logic has a single source of
 * truth.
 */

import { createSecret } from '../services/secretService'

// Config keys whose plaintext values are credentials and must be stored as
// encrypted tenant secrets rather than persisted in the connection JSON.
export const SECRET_FIELDS = new Set([
  'password',
  'secret',
  'api_key',
  'token',
  'auth_value',
  'private_key',
  'client_key',
  'secret_access_key',
  'account_key',
  'connection_string',
  'credentials_json',
  // Retail connectors (POS/ERP/OMS): Sitoo Basic-auth password, Front Systems
  // APIM subscription key, BC/Visma OAuth client secret, Brightpearl staff token.
  // (Front Systems / others' `api_key` is already covered above.)
  'api_password',
  'subscription_key',
  'client_secret',
  'staff_token',
])

/**
 * Recursively walk a node config; for every plaintext credential field, mint a
 * tenant secret and replace the plaintext with a `<field>_secret_id` reference.
 * Already-bound `<field>_secret_id` values and all other config are preserved.
 * Throws if a secret cannot be created (deploy aborts rather than persisting
 * plaintext).
 */
export async function materializeSecrets(value: unknown, hint: string): Promise<unknown> {
  if (Array.isArray(value)) {
    return Promise.all(value.map((v, i) => materializeSecrets(v, `${hint}-${i}`)))
  }
  if (value && typeof value === 'object') {
    const out: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      if (SECRET_FIELDS.has(k) && typeof v === 'string' && v !== '') {
        const secret = await createSecret(`${hint}-${k}-${Date.now()}`, v)
        out[`${k}_secret_id`] = secret.id
        // plaintext key intentionally dropped
      } else if (v === undefined) {
        // SecretInput clears a previously-bound `<field>_secret_id` by setting
        // it to undefined while the user types a replacement. Skip such keys —
        // otherwise this branch would clobber the `_secret_id` we just minted
        // above (when iteration reaches the undefined key after the plaintext
        // field), wiping BOTH the id and the plaintext on serialization.
        continue
      } else {
        out[k] = await materializeSecrets(v, hint)
      }
    }
    return out
  }
  return value
}
