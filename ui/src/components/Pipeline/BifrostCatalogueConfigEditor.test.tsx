/**
 * The Bifrost catalogue destination block (#815). These check it stores what
 * the connector reads (config.bifrost_catalogue), keeps the token as a field
 * the deploy step mints into a secret, and warns about full mode.
 */

import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { useState } from 'react'

vi.mock('../../services/api', () => ({ default: { post: vi.fn() } }))

import BifrostCatalogueConfigEditor from './BifrostCatalogueConfigEditor'
import { SECRET_FIELDS } from '../../utils/secrets'

function renderEditor(block: Record<string, unknown> = {}) {
  const onChange = vi.fn()
  function Host() {
    const [config, setConfig] = useState<Record<string, unknown>>({ type: 'bifrost_catalogue', bifrost_catalogue: block })
    return <BifrostCatalogueConfigEditor config={config} setConfig={(c) => { setConfig(c); onChange(c) }} />
  }
  render(<Host />)
  const last = () => onChange.mock.calls.at(-1)?.[0].bifrost_catalogue as Record<string, unknown>
  return { last }
}

describe('BifrostCatalogueConfigEditor', () => {
  it('stores the base URL, tenant id and batch size as the connector reads them', () => {
    const { last } = renderEditor()
    fireEvent.change(screen.getByLabelText(/base url/i), { target: { value: 'https://catalogue.example.com' } })
    fireEvent.change(screen.getByLabelText(/bifrost tenant id/i), { target: { value: 'nordic' } })
    fireEvent.change(screen.getByLabelText(/batch size/i), { target: { value: '500' } })
    expect(last()).toMatchObject({ base_url: 'https://catalogue.example.com', tenant_id: 'nordic', batch_size: 500 })
  })

  it('defaults to delta, and warns only for full', () => {
    const { last } = renderEditor()
    expect(screen.queryByText(/whole catalogue as one list/i)).toBeNull()
    fireEvent.change(screen.getByLabelText(/^import$/i), { target: { value: 'full' } })
    expect(last().mode).toBe('full')
    expect(screen.getByText(/whole catalogue as one list/i)).toBeTruthy()
  })

  it('clears the batch size when the box is emptied', () => {
    const { last } = renderEditor({ batch_size: 500 })
    fireEvent.change(screen.getByLabelText(/batch size/i), { target: { value: '' } })
    expect(last().batch_size).toBeUndefined()
  })

  it.each(['abc', '1.5', '-3', '0', ' '])('leaves the batch size unset for %j', (input) => {
    const { last } = renderEditor({ batch_size: 500 })
    fireEvent.change(screen.getByLabelText(/batch size/i), { target: { value: input } })
    expect(last().batch_size).toBeUndefined()
    expect(Number.isNaN(last().batch_size)).toBe(false)
  })

  it('keeps the token under a field the deploy step turns into a secret', () => {
    expect(SECRET_FIELDS.has('token')).toBe(true)
    renderEditor({ token_secret_id: '00000000-0000-0000-0000-000000000001' })
    expect(screen.getByText(/tenant token/i)).toBeTruthy()
  })
})
