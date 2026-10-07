/**
 * The trial / suspension banner (plans/paid-plans.md): what each billing
 * state shows, and that a paid workspace shows nothing.
 */

import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

let tenant: Record<string, unknown> | null = null
vi.mock('@/store/authStore', () => ({
  useAuthStore: (sel: (s: unknown) => unknown) => sel({ currentTenant: tenant }),
}))

import PlanBanner from './PlanBanner'
import { daysLeft } from '@/utils/plan'

const show = () => render(<MemoryRouter><PlanBanner /></MemoryRouter>)

describe('daysLeft', () => {
  it('rounds up and never goes negative', () => {
    const now = new Date('2026-10-07T12:00:00Z')
    expect(daysLeft('2026-10-21T08:00:00Z', now)).toBe(14)
    expect(daysLeft('2026-10-07T13:00:00Z', now)).toBe(1)
    expect(daysLeft('2026-10-01T00:00:00Z', now)).toBe(0)
    expect(daysLeft(undefined, now)).toBeNull()
    expect(daysLeft('not a date', now)).toBeNull()
  })
})

describe('PlanBanner', () => {
  it('counts the trial down and links to the plan page', () => {
    tenant = { id: 't', billing_status: 'trial', trial_ends_at: new Date(Date.now() + 5 * 86_400_000).toISOString() }
    show()
    expect(screen.getByRole('status')).toHaveTextContent(/Trial.*5 days left/)
    expect(screen.getByRole('link', { name: 'Choose a plan' })).toHaveAttribute('href', '/settings/plan')
  })

  it('says the trial ended and pipelines are stopped when suspended', () => {
    tenant = { id: 't', billing_status: 'suspended' }
    show()
    expect(screen.getByRole('status')).toHaveTextContent(/trial has ended.*Pipelines are stopped/)
    expect(screen.getByRole('link', { name: 'Request a plan' })).toHaveAttribute('href', '/settings/plan')
  })

  it('shows nothing for a paid workspace, or with no workspace', () => {
    tenant = { id: 't', billing_status: 'paid' }
    const { container } = show()
    expect(container).toBeEmptyDOMElement()
    tenant = null
    expect(show().container).toBeEmptyDOMElement()
  })
})
