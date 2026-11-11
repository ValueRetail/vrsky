/**
 * The NATS source block (#814). These check it stores what the connector
 * reads (config.nats), keeps a pasted .creds file's lines, and drops a saved
 * secret reference when a new value is typed so deploy mints a fresh one.
 */

import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { useState } from 'react'

vi.mock('../../services/api', () => ({ default: { post: vi.fn() } }))

import NatsConfigEditor from './NatsConfigEditor'

function renderEditor(nats: Record<string, unknown> = {}) {
  const onChange = vi.fn()
  function Host() {
    const [config, setConfig] = useState<Record<string, unknown>>({ type: 'nats', nats })
    return <NatsConfigEditor config={config} setConfig={(c) => { setConfig(c); onChange(c) }} />
  }
  render(<Host />)
  const last = () => onChange.mock.calls.at(-1)?.[0].nats as Record<string, unknown>
  return { onChange, last }
}

describe('NatsConfigEditor', () => {
  it('stores servers as a list and the stream, durable and filter', () => {
    const { last } = renderEditor()
    fireEvent.change(screen.getByLabelText(/server urls/i), { target: { value: 'tls://a.example.com:4222\ntls://b.example.com:4222' } })
    expect(last().servers).toEqual(['tls://a.example.com:4222', 'tls://b.example.com:4222'])
    fireEvent.change(screen.getByLabelText(/^stream$/i), { target: { value: 'RH_EVENTS' } })
    fireEvent.change(screen.getByLabelText(/durable/i), { target: { value: 'vrsky' } })
    fireEvent.change(screen.getByLabelText(/subject filter/i), { target: { value: 'rh.evt.v1.acme.>' } })
    expect(last()).toMatchObject({ stream: 'RH_EVENTS', durable: 'vrsky', filter_subject: 'rh.evt.v1.acme.>' })
  })

  it('lets a second server be typed one character at a time', () => {
    const { last } = renderEditor({ servers: ['tls://a.example.com'] })
    const box = screen.getByLabelText(/server urls/i) as HTMLTextAreaElement
    expect(box.value).toBe('tls://a.example.com')
    let text = box.value
    for (const ch of '\ntls://b') {
      text += ch
      fireEvent.change(box, { target: { value: text } })
      expect(box.value).toBe(text)
    }
    expect(last().servers).toEqual(['tls://a.example.com', 'tls://b'])
  })

  it('shows the credential fields for the chosen authentication only', () => {
    renderEditor()
    expect(screen.queryByLabelText(/password/i)).toBeNull()
    expect(screen.queryByLabelText(/credentials file/i)).toBeNull()

    fireEvent.change(screen.getByLabelText(/authentication/i), { target: { value: 'user_password' } })
    expect(screen.getByLabelText(/username/i)).toBeTruthy()
    expect(screen.getByText(/^password$/i)).toBeTruthy()

    fireEvent.change(screen.getByLabelText(/authentication/i), { target: { value: 'creds' } })
    expect(screen.getByLabelText(/credentials file/i)).toBeTruthy()
  })

  it('keeps the lines of a pasted .creds file and drops the old secret reference', () => {
    const { last } = renderEditor({ auth_type: 'creds', creds_secret_id: 'sec-1' })
    const area = screen.getByLabelText(/credentials file/i)
    expect(screen.getByText(/saved to the encrypted secrets vault/i)).toBeTruthy()

    const file = '-----BEGIN NATS USER JWT-----\neyJ0eXAi\n------END NATS USER JWT------\n'
    fireEvent.change(area, { target: { value: file } })
    expect(last().creds).toBe(file)
    expect(last().creds_secret_id).toBeUndefined()
    expect(screen.getByText(/stored encrypted .* when you deploy/i)).toBeTruthy()
  })

  it('stores the deliver policy and asks for a start time only when it needs one', () => {
    const { last } = renderEditor()
    expect(screen.queryByLabelText(/start time/i)).toBeNull()
    fireEvent.change(screen.getByLabelText(/first start reads/i), { target: { value: 'by_start_time' } })
    fireEvent.change(screen.getByLabelText(/start time/i), { target: { value: '2026-11-01T00:00:00Z' } })
    expect(last()).toMatchObject({ deliver_policy: 'by_start_time', opt_start_time: '2026-11-01T00:00:00Z' })
    fireEvent.change(screen.getByLabelText(/first start reads/i), { target: { value: 'new' } })
    expect(last().deliver_policy).toBe('new')
    expect(last().opt_start_time).toBeUndefined()
    expect(screen.queryByLabelText(/start time/i)).toBeNull()
  })

  it('stores a CA certificate', () => {
    const { last } = renderEditor()
    fireEvent.change(screen.getByLabelText(/ca certificate/i), { target: { value: '-----BEGIN CERTIFICATE-----' } })
    expect(last().ca_cert).toBe('-----BEGIN CERTIFICATE-----')
  })
})
