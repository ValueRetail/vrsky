/**
 * The `direct` source ("Published into VRSky"): an outside publisher, such as
 * Bifrost's Basket service (Bifrost ADR 0002), writes finished VRSky envelopes
 * into the main stream itself. Nothing is launched for it at start; the
 * pipeline workers take the messages from the connection's subject.
 */

/** Why the builder offers no sample or field discovery for this source: no
 *  connector of VRSky's reads it, so it never has a last payload, and the
 *  sample-data endpoint would fall back to another pipeline's. */
export const DIRECT_NO_SAMPLE =
  'An outside publisher writes into this source, so VRSky keeps no sample of it. Type the field paths by hand.'

/** The JetStream stream the publisher writes into. */
export const DIRECT_SOURCE_STREAM = 'VRSKY_DATA'

/**
 * The subject a publisher must use for this connection. The ids not known yet
 * (no workspace loaded, pipeline not deployed) are shown as placeholders.
 */
export function directSubject(tenantId?: string | null, connectionId?: string | null): string {
  return `vrsky.data.${tenantId || '<workspace id>'}.pipeline.${connectionId || '<connection id>'}`
}
