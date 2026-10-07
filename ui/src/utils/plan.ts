/**
 * Plan helpers shared by the banner, the Plan page and the operator page
 * (plans/paid-plans.md).
 */

import type { PlanName } from '@/types/models'

export const planTitles: Record<PlanName, string> = { trial: 'Trial', paid: 'Paid', enterprise: 'Enterprise' }

/** Whole days until trialEndsAt, never negative; null when unknown. */
export function daysLeft(trialEndsAt: string | undefined, now: Date = new Date()): number | null {
  if (!trialEndsAt) return null
  const ms = new Date(trialEndsAt).getTime() - now.getTime()
  if (Number.isNaN(ms)) return null
  return Math.max(0, Math.ceil(ms / 86_400_000))
}

/** A quota number for display; 0 means unlimited everywhere in VRSky. */
export function fmtLimit(n: number, unit = ''): string {
  if (n === 0) return 'Unlimited'
  return `${n.toLocaleString()}${unit}`
}

export function fmtStorage(bytes: number): string {
  if (bytes === 0) return 'Unlimited'
  const gib = bytes / 1024 ** 3
  return gib >= 1 ? `${Math.round(gib)} GiB` : `${Math.round(bytes / 1024 ** 2)} MiB`
}
