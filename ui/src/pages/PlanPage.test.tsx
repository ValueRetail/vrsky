/**
 * Settings → Plan (plans/paid-plans.md): the state the workspace is in, the
 * tiers, and that only an owner can ask — once.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

let role = 'owner'
vi.mock('@/store/authStore', () => ({
  useAuthStore: () => ({ currentTenant: { id: 'tenant-1', user_role: role } }),
}))
const getBilling = vi.fn()
const requestPlan = vi.fn()
vi.mock('@/services/billingService', () => ({
  getBilling: () => getBilling(),
  requestPlan: (...args: unknown[]) => requestPlan(...args),
}))

import PlanPage from './PlanPage'
import { fmtLimit, fmtStorage } from '@/utils/plan'

const plans = [
  { plan_name: 'trial', max_msg_per_sec: 25, max_integrations: 2, max_storage_bytes: 1 << 30, included_messages_per_month: 100000 },
  { plan_name: 'paid', max_msg_per_sec: 200, max_integrations: 20, max_storage_bytes: 100 * 2 ** 30, included_messages_per_month: 10000000 },
  { plan_name: 'enterprise', max_msg_per_sec: 0, max_integrations: 0, max_storage_bytes: 0, included_messages_per_month: 0 },
]
const trial = { plan: 'trial', billing_status: 'trial', trial_ends_at: new Date(Date.now() + 3 * 86_400_000).toISOString(), limits: plans[0], plans }

beforeEach(() => {
  role = 'owner'
  getBilling.mockReset()
  requestPlan.mockReset()
})

describe('formatting', () => {
  it('reads 0 as unlimited', () => {
    expect(fmtLimit(0)).toBe('Unlimited')
    expect(fmtLimit(200, ' msg/s')).toBe('200 msg/s')
    expect(fmtStorage(0)).toBe('Unlimited')
    expect(fmtStorage(100 * 2 ** 30)).toBe('100 GiB')
    expect(fmtStorage(512 * 2 ** 20)).toBe('512 MiB')
  })
})

describe('PlanPage', () => {
  it('shows the trial countdown, the tiers, and lets the owner ask for the paid plan with a message', async () => {
    getBilling.mockResolvedValue(trial)
    requestPlan.mockResolvedValue({ id: 'pr1', tenant_id: 'tenant-1', requested_plan: 'paid', message: 'two shops', created_at: new Date().toISOString() })
    render(<PlanPage />)

    expect(await screen.findByText(/On trial/)).toBeInTheDocument()
    expect(screen.getByText(/3 days left/)).toBeInTheDocument()
    expect(screen.getAllByText('Current')).toHaveLength(1)
    expect(screen.getByText('Enterprise')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Request the Paid plan' }))
    fireEvent.change(screen.getByLabelText('Message to VRSky'), { target: { value: 'two shops' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send request' }))

    await waitFor(() => expect(requestPlan).toHaveBeenCalledWith('tenant-1', 'paid', 'two shops'))
    expect(await screen.findByRole('status')).toHaveTextContent(/Requested: Paid/)
    // Once a request is open, the buttons are gone: one request at a time.
    expect(screen.queryByRole('button', { name: 'Request the Paid plan' })).toBeNull()
  })

  it('says why pipelines are stopped when suspended', async () => {
    getBilling.mockResolvedValue({ ...trial, billing_status: 'suspended', trial_ends_at: undefined })
    render(<PlanPage />)
    expect(await screen.findByText(/Trial ended — pipelines are stopped/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Request the Paid plan' })).toBeInTheDocument()
  })

  it('offers nothing to ask for to a non-owner, and shows an open request to everyone', async () => {
    role = 'editor'
    getBilling.mockResolvedValue({ ...trial, open_request: { id: 'pr1', tenant_id: 'tenant-1', requested_plan: 'enterprise', message: '', created_at: new Date().toISOString() } })
    render(<PlanPage />)
    expect(await screen.findByRole('status')).toHaveTextContent(/Requested: Enterprise/)
    expect(screen.queryByRole('button', { name: /Request|Talk to us/ })).toBeNull()
  })

  it('tells a non-owner without a request who can ask', async () => {
    role = 'viewer'
    getBilling.mockResolvedValue(trial)
    render(<PlanPage />)
    expect(await screen.findByText('Only the workspace owner can request a plan.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Request|Talk to us/ })).toBeNull()
  })

  it('shows a paid workspace as active with nothing to do', async () => {
    getBilling.mockResolvedValue({ ...trial, plan: 'paid', billing_status: 'paid', trial_ends_at: undefined, limits: plans[1] })
    render(<PlanPage />)
    expect(await screen.findByText(/Paid plan/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Request the Paid plan' })).toBeNull()
  })

  it('shows the error when the request fails, and keeps the form', async () => {
    getBilling.mockResolvedValue(trial)
    requestPlan.mockRejectedValue(new Error('network down'))
    render(<PlanPage />)
    fireEvent.click(await screen.findByRole('button', { name: 'Request the Paid plan' }))
    fireEvent.click(screen.getByRole('button', { name: 'Send request' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('network down')
    expect(screen.getByRole('button', { name: 'Send request' })).toBeInTheDocument()
  })

  it('reports a failed load', async () => {
    getBilling.mockRejectedValue(new Error('boom'))
    render(<PlanPage />)
    expect(await screen.findByText('boom')).toBeInTheDocument()
  })
})
