/**
 * The `direct` source panel. It has no settings, so the one thing it must get
 * right is the subject the outside publisher writes to: with the real
 * workspace and connection ids when they are known, and placeholders that
 * cannot be mistaken for ids when they are not.
 */

import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'

let tenant: { id: string } | null = { id: 'ws-123' }
vi.mock('../../store/authStore', () => ({
  useAuthStore: (sel: (s: unknown) => unknown) => sel({ currentTenant: tenant }),
}))

import DirectSourceInfo from './DirectSourceInfo'
import { directSubject } from './directSource'

describe('directSubject', () => {
  it('is the pipeline subject of the workspace and connection', () => {
    expect(directSubject('ws-123', 'conn-9')).toBe('vrsky.data.ws-123.pipeline.conn-9')
  })

  it('shows placeholders for the ids it does not know yet', () => {
    expect(directSubject(undefined, null)).toBe('vrsky.data.<workspace id>.pipeline.<connection id>')
  })
})

describe('DirectSourceInfo', () => {
  it('shows a deployed pipeline the stream and its real subject', () => {
    tenant = { id: 'ws-123' }
    render(<DirectSourceInfo deployedConnectionId="conn-9" />)
    expect(screen.getByText('VRSKY_DATA')).toBeInTheDocument()
    expect(screen.getByTestId('direct-subject')).toHaveTextContent('vrsky.data.ws-123.pipeline.conn-9')
    expect(screen.getByRole('button', { name: /copy/i })).toBeInTheDocument()
    expect(screen.queryByText(/deploy the pipeline/i)).not.toBeInTheDocument()
  })

  it('asks for a deploy before there is a connection id, and offers nothing to copy', () => {
    tenant = { id: 'ws-123' }
    render(<DirectSourceInfo />)
    expect(screen.getByTestId('direct-subject')).toHaveTextContent('vrsky.data.ws-123.pipeline.<connection id>')
    expect(screen.queryByRole('button', { name: /copy/i })).not.toBeInTheDocument()
    expect(screen.getByText(/deploy the pipeline to get its connection id/i)).toBeInTheDocument()
  })

  it('warns that stopping the pipeline does not stop the publisher', () => {
    tenant = { id: 'ws-123' }
    render(<DirectSourceInfo deployedConnectionId="conn-9" />)
    expect(screen.getByText(/stopping this pipeline does not stop deliveries/i)).toBeInTheDocument()
  })

  it('does not invent a workspace id when none is loaded', () => {
    tenant = null
    render(<DirectSourceInfo deployedConnectionId="conn-9" />)
    expect(screen.getByTestId('direct-subject')).toHaveTextContent('vrsky.data.<workspace id>.pipeline.conn-9')
    expect(screen.queryByRole('button', { name: /copy/i })).not.toBeInTheDocument()
  })
})

