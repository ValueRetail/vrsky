/**
 * Per-tenant "has finished/skipped onboarding" flag (Phase 4B / #93). Stored in
 * localStorage so the first-login redirect to the wizard fires at most once per
 * tenant per browser. No server state — onboarding is a UI affordance.
 */

const key = (tenantId: string) => `vrsky:onboarded:${tenantId}`

export function markOnboarded(tenantId: string | undefined): void {
  if (tenantId) localStorage.setItem(key(tenantId), '1')
}

export function isOnboarded(tenantId: string | undefined): boolean {
  return !!tenantId && localStorage.getItem(key(tenantId)) === '1'
}
