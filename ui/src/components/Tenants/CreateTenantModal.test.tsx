/**
 * Creating a workspace from the header: name validation, the create request,
 * errors, and the provisioning progress streamed from
 * GET /api/v1/tenants/:id/status/stream (useTenantStatus, ProvisioningSpinner)
 * until the workspace is ready or setup fails.
 *
 * The create goes through the mocked HTTP client. The status stream uses
 * fetch, so fetch is stubbed with a body whose reader the test feeds.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'

vi.mock(import('@/services/api'), async (importOriginal) => ({
  ...(await importOriginal()),
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } as never,
}))

import apiClient from '@/services/api'
import { useAuthStore } from '@/store/authStore'
import { config } from '@/config/env'
import CreateTenantModal from './CreateTenantModal'

const post = vi.mocked(apiClient.post)
const SESSION_KEY = 'vrsky_session_token'

// A server-sent-events body the test writes to, chunk by chunk.
const sseStream = () => {
  const encoder = new TextEncoder()
  const queue: { done: boolean; value?: Uint8Array }[] = []
  let wake: (() => void) | null = null
  const push = (item: { done: boolean; value?: Uint8Array }) => {
    queue.push(item)
    wake?.()
    wake = null
  }
  const reader = {
    read: async () => {
      while (queue.length === 0) await new Promise<void>((r) => { wake = r })
      return queue.shift()!
    },
  }
  return {
    response: { ok: true, body: { getReader: () => reader } },
    send: (text: string) => push({ done: false, value: encoder.encode(text) }),
    event: (data: unknown) => push({ done: false, value: encoder.encode(`data: ${JSON.stringify({ type: 'status', data })}\n`) }),
    end: () => push({ done: true }),
  }
}

const fetchMock = vi.fn()
const onClose = vi.fn()

beforeEach(() => {
  post.mockReset()
  fetchMock.mockReset()
  onClose.mockReset()
  vi.stubGlobal('fetch', fetchMock)
  useAuthStore.setState(useAuthStore.getInitialState(), true)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

const show = (isOpen = true) => render(<CreateTenantModal isOpen={isOpen} onClose={onClose} />)
const nameInput = () => screen.getByLabelText('Workspace name')
const submit = () => fireEvent.click(screen.getByRole('button', { name: 'Create workspace' }))

describe('CreateTenantModal form', () => {
  it('renders nothing when closed', () => {
    const { container } = show(false)
    expect(container).toBeEmptyDOMElement()
  })

  it('refuses a name shorter than 3 characters without a request', () => {
    show()
    fireEvent.change(nameInput(), { target: { value: ' ab ' } })
    submit()
    expect(screen.getByText('Workspace name must be at least 3 characters')).toBeInTheDocument()
    expect(post).not.toHaveBeenCalled()
  })

  it('Cancel closes the dialog', () => {
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('shows why the create failed and lets the user try again', async () => {
    post.mockRejectedValueOnce(new Error('workspace limit reached'))
    show()
    fireEvent.change(nameInput(), { target: { value: 'Acme Corp' } })
    submit()

    expect(await screen.findByText('Failed to create workspace: workspace limit reached')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Create workspace' })).toBeEnabled()
    expect(onClose).not.toHaveBeenCalled()
  })

  it('a failure without a message still says the create failed', async () => {
    post.mockRejectedValueOnce('nope')
    show()
    fireEvent.change(nameInput(), { target: { value: 'Acme Corp' } })
    submit()
    expect(await screen.findByText('Failed to create workspace')).toBeInTheDocument()
  })

  it('sends the trimmed name and closes when no provisioning is needed', async () => {
    post.mockResolvedValueOnce({ data: { tenant: {} } } as never)
    show()
    fireEvent.change(nameInput(), { target: { value: '  Acme Corp  ' } })
    submit()

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/v1/tenants', { name: 'Acme Corp' })
    expect(fetchMock).not.toHaveBeenCalled()
    // The workspace list is refreshed (no session here, so it comes back empty).
    expect(useAuthStore.getState().isInitialized).toBe(true)
  })
})

describe('CreateTenantModal provisioning progress', () => {
  const startProvisioning = async () => {
    sessionStorage.setItem(SESSION_KEY, 'test-session-token')
    post.mockResolvedValueOnce({ data: { tenant: { id: 'tenant-new' } } } as never)
    show()
    fireEvent.change(nameInput(), { target: { value: 'Acme Corp' } })
    submit()
    expect(await screen.findByRole('heading', { name: 'Setting up workspace...' })).toBeInTheDocument()
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
  }

  it('streams the status with the session token and shows progress, then closes once ready', async () => {
    const stream = sseStream()
    fetchMock.mockResolvedValue(stream.response)
    await startProvisioning()

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(`${config.apiUrl}/api/v1/tenants/tenant-new/status/stream`)
    expect(init.headers).toMatchObject({ Authorization: 'Bearer test-session-token', Accept: 'text/event-stream' })
    expect(screen.getByText('Initializing...')).toBeInTheDocument()
    expect(screen.getByText('0% complete')).toBeInTheDocument()

    // An event split across two chunks is put back together.
    const event = `data: ${JSON.stringify({ type: 'status', data: { tenant_id: 'tenant-new', status: 'provisioning', progress: 40, current_step: 'Creating NATS account' } })}\n`
    await act(async () => {
      stream.send(event.slice(0, 20))
      stream.send(event.slice(20))
    })
    expect(await screen.findByText('Creating NATS account')).toBeInTheDocument()
    expect(screen.getByText('40% complete')).toBeInTheDocument()

    // The refresh after completion must not reach for a session.
    sessionStorage.removeItem(SESSION_KEY)
    await act(async () => {
      stream.event({ tenant_id: 'tenant-new', status: 'active', progress: 100, current_step: 'Done' })
    })
    expect(await screen.findByText('Workspace ready!')).toBeInTheDocument()
    await waitFor(() => expect(onClose).toHaveBeenCalled(), { timeout: 3000 })
  })

  it('ignores events that are not status updates or not JSON', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    const stream = sseStream()
    fetchMock.mockResolvedValue(stream.response)
    await startProvisioning()

    await act(async () => {
      stream.send('data: {not json\n')
      stream.send(`data: ${JSON.stringify({ type: 'ping' })}\n`)
      stream.send(': keep-alive\n')
      stream.event({ tenant_id: 'tenant-new', status: 'provisioning', progress: 70, current_step: 'Starting NATS' })
    })
    expect(await screen.findByText('Starting NATS')).toBeInTheDocument()
    expect(consoleError).toHaveBeenCalled()
    consoleError.mockRestore()
  })

  it('shows why setup failed', async () => {
    const stream = sseStream()
    fetchMock.mockResolvedValue(stream.response)
    await startProvisioning()

    await act(async () => {
      stream.event({ tenant_id: 'tenant-new', status: 'failed', progress: 50, current_step: 'x', error: 'quota exceeded' })
      stream.end()
    })
    expect(await screen.findByText('Setup failed: quota exceeded')).toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
  })

  it('a failure without a reason says so', async () => {
    const stream = sseStream()
    fetchMock.mockResolvedValue(stream.response)
    await startProvisioning()
    await act(async () => {
      stream.event({ tenant_id: 'tenant-new', status: 'failed', progress: 0, current_step: '' })
    })
    expect(await screen.findByText('Setup failed: Unknown error')).toBeInTheDocument()
  })

  it('keeps waiting when the stream cannot be opened', async () => {
    fetchMock.mockResolvedValue({ ok: false, body: null })
    await startProvisioning()
    expect(screen.getByText('Initializing...')).toBeInTheDocument()
  })

  it('keeps waiting when the connection drops', async () => {
    fetchMock.mockRejectedValue(new TypeError('network down'))
    await startProvisioning()
    expect(screen.getByText('Initializing...')).toBeInTheDocument()
  })

  it('does not open the stream without a session', async () => {
    post.mockResolvedValueOnce({ data: { tenant: { id: 'tenant-new' } } } as never)
    show()
    fireEvent.change(nameInput(), { target: { value: 'Acme Corp' } })
    submit()
    expect(await screen.findByRole('heading', { name: 'Setting up workspace...' })).toBeInTheDocument()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('stops the stream when the dialog goes away', async () => {
    const stream = sseStream()
    fetchMock.mockResolvedValue(stream.response)
    sessionStorage.setItem(SESSION_KEY, 'test-session-token')
    post.mockResolvedValueOnce({ data: { tenant: { id: 'tenant-new' } } } as never)
    const { unmount } = show()
    fireEvent.change(nameInput(), { target: { value: 'Acme Corp' } })
    submit()
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))

    const signal = (fetchMock.mock.calls[0][1] as RequestInit).signal!
    expect(signal.aborted).toBe(false)
    unmount()
    expect(signal.aborted).toBe(true)
  })
})
