/**
 * 402 PlanRequired (plans/paid-plans.md): the workspace's trial ended and its
 * pipelines are stopped. The failing request still fails; this tells the user
 * once what to do about it. Kept out of api.ts so the store import does not
 * pull the client into a cycle, mirroring sessionExpiry.
 */

import { useUIStore } from '@/store/uiStore'

const QUIET_MS = 15_000
let lastShown = 0

export function reportPlanRequired(now: number = Date.now()): boolean {
  if (now - lastShown < QUIET_MS) return false
  lastShown = now
  useUIStore.getState().addNotification({
    type: 'warning',
    title: 'Your workspace needs a plan',
    message: 'The trial has ended and pipelines are stopped. Ask for a plan under Settings → Plan.',
    duration: 10_000,
  })
  return true
}

/** For tests. */
export function resetPlanRequired(): void {
  lastShown = 0
}
