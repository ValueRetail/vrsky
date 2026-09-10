/**
 * The per-tenant "has finished onboarding" flag.
 *
 * It decides whether a returning user is dropped into the wizard again. Getting
 * it wrong is not dramatic, but it is the difference between a returning user
 * landing on their dashboard and being sent back to a first-run screen.
 */

import { describe, it, expect, beforeEach } from 'vitest'
import { isOnboarded, markOnboarded } from './state'

describe('onboarded flag', () => {
  beforeEach(() => localStorage.clear())

  it('is false before anything is marked', () => {
    expect(isOnboarded('tenant-a')).toBe(false)
  })

  it('is true after marking, and survives a re-read', () => {
    markOnboarded('tenant-a')
    expect(isOnboarded('tenant-a')).toBe(true)
    expect(isOnboarded('tenant-a')).toBe(true)
  })

  it('is scoped per tenant', () => {
    // Two workspaces in one browser is the normal case for anyone running more
    // than one. Marking one must not skip the wizard for the other.
    markOnboarded('tenant-a')
    expect(isOnboarded('tenant-b')).toBe(false)
  })

  it('treats a missing tenant id as not onboarded, and never writes for one', () => {
    // currentTenant is undefined while the session is still loading. Writing
    // then would store a `vrsky:onboarded:undefined` key that no tenant reads.
    markOnboarded(undefined)
    expect(isOnboarded(undefined)).toBe(false)
    expect(localStorage.length).toBe(0)
  })
})
