/**
 * The `direct` source ("Published into VRSky"): an outside publisher, such as
 * Bifrost's Basket service (Bifrost ADR 0002), writes finished VRSky envelopes
 * into the main stream itself. Nothing is launched for it at start; the
 * pipeline workers take the messages from the connection's subject.
 */

/** The JetStream stream the publisher writes into. */
export const DIRECT_SOURCE_STREAM = 'VRSKY_DATA'

/**
 * The subject a publisher must use for this connection. The ids not known yet
 * (no workspace loaded, pipeline not deployed) are shown as placeholders.
 */
export function directSubject(tenantId?: string | null, connectionId?: string | null): string {
  return `vrsky.data.${tenantId || '<workspace id>'}.pipeline.${connectionId || '<connection id>'}`
}
