/**
 * Platform → Workspaces (plans/paid-plans.md): the operator's list and the one
 * write. The server decides who is an operator; this checks what the page
 * sends and shows.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

const listTenants = vi.fn()
const setPlan = vi.fn()
vi.mock('@/services/platformService', () => ({
  listTenants: () => listTenants(),
  setPlan: (id: string, change: unknown) => setPlan(id, change),
}))

import PlatformTenantsPage from './PlatformTenantsPage'

const shop = {
  id: 't1', name: 'Shop', slug: 'shop', owner_email: 'owner@shop.test', plan: 'trial', billing_status: 'suspended',
  billing_note: '', created_at: '2026-09-20T10:00:00Z', messages_30d: 1234, running_pipelines: 0,
  open_request: { id: 'pr1', tenant_id: 't1', requested_plan: 'paid', message: 'please', created_at: '2026-10-06T09:00:00Z' },
}
const paying = {
  id: 't2', name: 'Paying', slug: 'paying', owner_email: 'o@p.test', plan: 'paid', billing_status: 'paid',
  billing_note: 'inv 7', created_at: '2026-08-01T10:00:00Z', messages_30d: 99000, running_pipelines: 3,
}

beforeEach(() => {
  listTenants.mockReset()
  setPlan.mockReset()
})

describe('PlatformTenantsPage', () => {
  it('lists every workspace with its state and open request', async () => {
    listTenants.mockResolvedValue([shop, paying])
    render(<PlatformTenantsPage />)
    expect(await screen.findByText('Shop')).toBeInTheDocument()
    expect(screen.getByText('Suspended')).toBeInTheDocument()
    expect(screen.getByText(/Wants Paid/)).toBeInTheDocument()
    expect(screen.getByText('please')).toBeInTheDocument()
    expect(screen.getByText('Paying')).toBeInTheDocument()
    expect(screen.getByText('99,000')).toBeInTheDocument()
    expect(screen.getByText(/1 open request/)).toBeInTheDocument()
  })

  it('sets a plan and reports what came back, then reloads', async () => {
    listTenants.mockResolvedValueOnce([shop]).mockResolvedValueOnce([{ ...shop, plan: 'paid', billing_status: 'paid', open_request: undefined }])
    setPlan.mockResolvedValue({ tenant_id: 't1', plan: 'paid', billing_status: 'paid', pipelines_resumed: 2 })
    render(<PlatformTenantsPage />)

    const select = await screen.findByLabelText('Plan for Shop')
    const save = screen.getByRole('button', { name: 'Save' })
    expect(save).toBeDisabled() // nothing changed yet
    fireEvent.change(select, { target: { value: 'paid' } })
    fireEvent.change(screen.getByLabelText('Note for Shop'), { target: { value: 'invoice 2026-041' } })
    fireEvent.click(save)

    await waitFor(() => expect(setPlan).toHaveBeenCalledWith('t1', { plan: 'paid', trial_ends_at: undefined, note: 'invoice 2026-041' }))
    expect(await screen.findByRole('status')).toHaveTextContent(/Shop: Paid \(paid\), 2 pipeline\(s\) started again/)
    await waitFor(() => expect(listTenants).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.queryByText('Suspended')).toBeNull())
    expect(screen.queryByText(/Wants Paid/)).toBeNull()
  })

  it('sends a trial end date as the end of that day', async () => {
    listTenants.mockResolvedValue([paying])
    setPlan.mockResolvedValue({ tenant_id: 't2', plan: 'trial', billing_status: 'trial', pipelines_resumed: 0 })
    render(<PlatformTenantsPage />)
    fireEvent.change(await screen.findByLabelText('Plan for Paying'), { target: { value: 'trial' } })
    fireEvent.change(screen.getByLabelText('Trial end for Paying'), { target: { value: '2026-12-01' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(setPlan).toHaveBeenCalledWith('t2', { plan: 'trial', trial_ends_at: '2026-12-01T23:59:59.000Z', note: undefined }))
  })

  it('shows the server error when a save fails', async () => {
    listTenants.mockResolvedValue([paying])
    setPlan.mockRejectedValue(new Error('platform operators only'))
    render(<PlatformTenantsPage />)
    fireEvent.change(await screen.findByLabelText('Plan for Paying'), { target: { value: 'enterprise' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('platform operators only')
  })

  it('reports a failed load', async () => {
    listTenants.mockRejectedValue(new Error('403'))
    render(<PlatformTenantsPage />)
    expect(await screen.findByRole('alert')).toHaveTextContent('403')
  })
})
