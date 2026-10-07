/**
 * The deploy sequence of the pipeline builder (plans/stable-connections.md).
 *
 * A connection id is a contract — a webhook URL carries it — so a redeploy
 * keeps the id: stop, update in place, start. When the connection the canvas
 * deployed no longer exists on the server, this does NOT quietly make a new
 * one: it reports ConnectionGone and the page asks. Any other failure of the
 * update is an error, not a reason to create.
 */

import type { AxiosInstance } from 'axios'

export class ConnectionGone extends Error {
  readonly connectionId: string

  constructor(connectionId: string) {
    super(`connection ${connectionId} no longer exists on the server`)
    this.name = 'ConnectionGone'
    this.connectionId = connectionId
  }
}

export interface DeployResult {
  connectionId: string
  /** true when a connection was created rather than updated */
  created: boolean
}

type Client = Pick<AxiosInstance, 'post' | 'put'>

function statusOf(err: unknown): number | undefined {
  const e = err as { response?: { status?: number }; status?: number; details?: { status?: number } }
  return e?.response?.status ?? e?.details?.status ?? e?.status
}

/**
 * Deploys the payload onto previousConnectionId, or creates a connection when
 * there is none. Throws ConnectionGone when the previous one has disappeared;
 * the caller decides whether to create.
 */
export async function deployPipeline(
  client: Client,
  payload: unknown,
  previousConnectionId: string | undefined,
): Promise<DeployResult> {
  if (!previousConnectionId) {
    return { connectionId: await create(client, payload), created: true }
  }
  try {
    await client.post(`/api/v1/connections/${previousConnectionId}/stop`)
  } catch (err) {
    if (statusOf(err) === 404) throw new ConnectionGone(previousConnectionId)
    // Already stopped, or a transient failure: the update below decides.
  }
  try {
    await client.put(`/api/v1/connections/${previousConnectionId}`, payload)
  } catch (err) {
    if (statusOf(err) === 404) throw new ConnectionGone(previousConnectionId)
    throw err
  }
  return { connectionId: previousConnectionId, created: false }
}

/** Creates a new connection for the payload; the "deploy as new" path. */
export async function create(client: Client, payload: unknown): Promise<string> {
  const response = await client.post('/api/v1/connections', payload)
  const id = response.data?.data?.id as string | undefined
  if (!id) throw new Error('No connection ID returned from server')
  return id
}
