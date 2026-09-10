/**
 * The onboarding wizard — the path a non-developer takes to their first
 * pipeline, and the one docs/tutorials/first-pipeline.md promises finishes in
 * under ten minutes.
 *
 * These cover the steps where a failure is silent rather than loud: a Deploy
 * button that submits an unconfigured pipeline, a credential that reaches the
 * API in plaintext, or a webhook template that deploys and then never shows the
 * user the URL it just created.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

const navigate = vi.fn()
vi.mock('react-router-dom', () => ({ useNavigate: () => navigate }))

const post = vi.fn()
vi.mock('../services/api', () => ({ default: { post: (...a: unknown[]) => post(...a) } }))

vi.mock('../config/env', () => ({ config: { webhookIngressUrl: 'https://hooks.test' } }))

vi.mock('../store/authStore', () => ({
  useAuthStore: (sel: (s: unknown) => unknown) => sel({ currentTenant: { id: 'tenant-1' } }),
}))

// The real one calls the secrets API. What matters here is that the wizard
// routes config through it rather than posting typed credentials directly, so
// this records what it was handed and marks the value as bound.
const materialize = vi.fn(async (cfg: Record<string, unknown>, _nodeId: string) => {
  const out = JSON.parse(JSON.stringify(cfg))
  for (const k of Object.keys(out)) {
    const v = out[k]
    if (v && typeof v === 'object') {
      for (const inner of Object.keys(v as Record<string, unknown>)) {
        if (inner === 'api_password' || inner === 'client_secret' || inner === 'connection_string') {
          delete (v as Record<string, unknown>)[inner]
          ;(v as Record<string, unknown>)[`${inner}_secret_id`] = 'sec-123'
        }
      }
    }
  }
  return out
})
vi.mock('../utils/secrets', async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  materializeSecrets: (cfg: Record<string, unknown>, nodeId: string) => materialize(cfg, nodeId),
}))

import OnboardingWizard from './OnboardingWizard'
import { TEMPLATES } from './templates'
import { isOnboarded } from './state'

// Pick by button rather than by text: "Webhook → Slack" is both a template
// name and, on the same card, its own "sourceLabel → destLabel" line — so
// getByText matches twice for it.
const pick = (name: string) => {
  const card = screen
    .getAllByRole('button')
    .find((b) => b.textContent?.includes(name))
  if (!card) throw new Error(`no template card for ${name}`)
  fireEvent.click(card)
}

// Fills every input on the configure step, including the masked SecretInputs,
// which are type=password and so have no `textbox` role.
const fillEveryField = (container: HTMLElement) => {
  container.querySelectorAll('input').forEach((el, i) => {
    fireEvent.change(el, { target: { value: `v${i}` } })
  })
}

const deployButton = () => screen.getByRole('button', { name: /deploy pipeline/i })

beforeEach(() => {
  vi.clearAllMocks()
  post.mockResolvedValue({ data: { data: { id: 'conn-9' } } })
})

describe('the gallery', () => {
  it('offers every template, so none is silently unreachable', () => {
    render(<OnboardingWizard />)
    // getAllByText: see the note on `pick` — one template's name repeats inside
    // its own card.
    for (const t of TEMPLATES) expect(screen.getAllByText(t.name).length).toBeGreaterThan(0)
  })
})

describe('required fields gate the deploy', () => {
  it('disables Deploy until every field is filled, and names what is missing', () => {
    render(<OnboardingWizard />)
    pick('Webhook → HTTP endpoint')

    // The template needs a destination URL and ships without one.
    expect(deployButton()).toBeDisabled()
    expect(screen.getByText(/^Fill in:/)).toHaveTextContent('Destination URL')

    fireEvent.change(screen.getByDisplayValue(''), { target: { value: 'https://example.test/hook' } })
    expect(deployButton()).toBeEnabled()
  })

  it('does not accept whitespace as a filled field', () => {
    // `missing` trims. Without that a user could tab through with spaces and
    // deploy a producer pointed at nothing.
    render(<OnboardingWizard />)
    pick('Webhook → HTTP endpoint')
    fireEvent.change(screen.getByDisplayValue(''), { target: { value: '   ' } })
    expect(deployButton()).toBeDisabled()
  })
})

describe('deploying', () => {
  it('posts a connection whose nodes and edges came from the template', async () => {
    render(<OnboardingWizard />)
    pick('Webhook → HTTP endpoint')
    fireEvent.change(screen.getByDisplayValue(''), { target: { value: 'https://example.test/hook' } })
    fireEvent.click(deployButton())

    await waitFor(() => expect(post).toHaveBeenCalled())
    const [url, payload] = post.mock.calls[0] as [string, Record<string, unknown>]
    expect(url).toBe('/api/v1/connections')

    const tpl = TEMPLATES.find((t) => t.name === 'Webhook → HTTP endpoint')!
    const nodes = payload.nodes as { id: string; type: string; enabled: boolean }[]
    expect(nodes.map((n) => n.id)).toEqual(tpl.nodes.map((n) => n.id))
    expect(nodes.every((n) => n.enabled)).toBe(true)
    expect((payload.edges as unknown[]).length).toBe(tpl.edges.length)
  })

  it('routes every node config through materializeSecrets', async () => {
    // The wizard must not post what the user typed. This is the same path the
    // visual builder uses; skipping it would persist credentials in plaintext.
    const { container } = render(<OnboardingWizard />)
    pick('POS sales → ERP (Sitoo → Business Central)')
    fillEveryField(container)

    await waitFor(() => expect(deployButton()).toBeEnabled())
    fireEvent.click(deployButton())
    await waitFor(() => expect(post).toHaveBeenCalled())

    const tpl = TEMPLATES.find((t) => t.id === 'sitoo-to-business-central')!
    expect(materialize).toHaveBeenCalledTimes(tpl.nodes.length)

    // Nothing that went out may still carry a raw secret key.
    const body = JSON.stringify(post.mock.calls[0][1])
    for (const f of tpl.fields.filter((f) => f.secret)) {
      expect(body, `${f.key} left the browser unencrypted`).not.toContain(`"${f.key}":`)
    }
  })

  it('starts the connection it just created', async () => {
    render(<OnboardingWizard />)
    pick('Webhook → HTTP endpoint')
    fireEvent.change(screen.getByDisplayValue(''), { target: { value: 'https://example.test/hook' } })
    fireEvent.click(deployButton())

    await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
    expect(post.mock.calls[1][0]).toBe('/api/v1/connections/conn-9/start')
  })

  it('still completes when auto-start fails', async () => {
    // Start is best-effort: the connection exists either way, and dropping the
    // user on an error screen would strand a pipeline they cannot see.
    post.mockResolvedValueOnce({ data: { data: { id: 'conn-9' } } }).mockRejectedValueOnce(new Error('boom'))
    render(<OnboardingWizard />)
    pick('Webhook → HTTP endpoint')
    fireEvent.change(screen.getByDisplayValue(''), { target: { value: 'https://example.test/hook' } })
    fireEvent.click(deployButton())

    expect(await screen.findByText(/is live/)).toBeInTheDocument()
  })

  it('surfaces a failure instead of pretending it worked', async () => {
    post.mockRejectedValueOnce(new Error('server said no'))
    render(<OnboardingWizard />)
    pick('Webhook → HTTP endpoint')
    fireEvent.change(screen.getByDisplayValue(''), { target: { value: 'https://example.test/hook' } })
    fireEvent.click(deployButton())

    expect(await screen.findByText('server said no')).toBeInTheDocument()
    expect(navigate).not.toHaveBeenCalled()
  })

  it('treats a response with no id as a failure', async () => {
    // A 200 with an unexpected body would otherwise mark the user onboarded and
    // navigate to /connections/undefined/edit.
    post.mockResolvedValueOnce({ data: {} })
    render(<OnboardingWizard />)
    pick('Webhook → HTTP endpoint')
    fireEvent.change(screen.getByDisplayValue(''), { target: { value: 'https://example.test/hook' } })
    fireEvent.click(deployButton())

    expect(await screen.findByText(/No connection ID/i)).toBeInTheDocument()
    expect(isOnboarded('tenant-1')).toBe(false)
  })
})

describe('after a successful deploy', () => {
  it('shows a webhook template its public URL, rather than navigating away', async () => {
    render(<OnboardingWizard />)
    pick('Webhook → HTTP endpoint')
    fireEvent.change(screen.getByDisplayValue(''), { target: { value: 'https://example.test/hook' } })
    fireEvent.click(deployButton())

    expect(await screen.findByText('https://hooks.test/webhook/conn-9')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /send a sample event/i })).toBeInTheDocument()
    expect(navigate).not.toHaveBeenCalled()
  })

  it('sends a non-webhook template straight to the canvas', async () => {
    const { container } = render(<OnboardingWizard />)
    pick('API poll → File')
    fillEveryField(container)
    await waitFor(() => expect(deployButton()).toBeEnabled())
    fireEvent.click(deployButton())

    await waitFor(() => expect(navigate).toHaveBeenCalledWith('/connections/conn-9/edit'))
  })

  it('marks the tenant onboarded so the wizard does not reappear', async () => {
    render(<OnboardingWizard />)
    pick('Webhook → HTTP endpoint')
    fireEvent.change(screen.getByDisplayValue(''), { target: { value: 'https://example.test/hook' } })
    fireEvent.click(deployButton())

    await waitFor(() => expect(isOnboarded('tenant-1')).toBe(true))
  })
})

describe('going back', () => {
  it('returns to the gallery with the picked template forgotten', () => {
    render(<OnboardingWizard />)
    pick('Webhook → HTTP endpoint')
    fireEvent.click(screen.getByRole('button', { name: /back/i }))

    expect(screen.queryByRole('button', { name: /deploy pipeline/i })).toBeNull()
    // getAllByText: see the note on `pick` — one template's name repeats inside
    // its own card.
    for (const t of TEMPLATES) expect(screen.getAllByText(t.name).length).toBeGreaterThan(0)
  })
})
