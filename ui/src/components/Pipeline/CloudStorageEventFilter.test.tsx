/**
 * Event mode applies prefix and file_pattern since #594, so the source panel
 * shows both fields in event mode too. A pattern saved in poll mode stays
 * visible after a switch to event mode, where it now skips objects.
 */

import { describe, it, expect } from 'vitest'
import { useState } from 'react'
import { render, screen, fireEvent } from '@testing-library/react'
import CloudStorageConfigEditor from './CloudStorageConfigEditor'

type Cfg = Record<string, unknown>
type Block = { file_pattern?: string; prefix?: string; mode?: string } | undefined

function mount(config: Cfg, role: 'consumer' | 'producer' = 'consumer') {
  const latest = { current: config }
  function Host({ initial }: { initial: Cfg }) {
    const [c, setC] = useState<Cfg>(initial)
    const setConfig = (next: Cfg) => { latest.current = next; setC(next) }
    return <CloudStorageConfigEditor config={c} setConfig={setConfig} role={role} />
  }
  render(<Host initial={config} />)
  return { latest }
}

const patternInput = () => screen.queryByLabelText('File pattern (optional)') as HTMLInputElement | null
const prefixInput = () => screen.getByLabelText('Prefix (optional)') as HTMLInputElement
const csOf = (l: { current: Cfg }) => l.current.cloud_storage as Block
const modeSelect = () =>
  Array.from(document.querySelectorAll('select'))
    .find((s) => Array.from(s.options).some((o) => o.value === 'event')) as HTMLSelectElement

describe('cloud-storage source file pattern and prefix', () => {
  it('shows a stored pattern and prefix in event mode', () => {
    mount({ type: 'cloud_storage', cloud_storage: { mode: 'event', file_pattern: '*.csv', prefix: 'in/' } })
    expect(patternInput()).not.toBeNull()
    expect(patternInput()!.value).toBe('*.csv')
    expect(prefixInput().value).toBe('in/')
  })

  it('says in event mode that the pattern now applies there', () => {
    mount({ type: 'cloud_storage', cloud_storage: { mode: 'event' } })
    const help = document.getElementById(patternInput()!.getAttribute('aria-describedby')!)
    expect(help?.textContent).toMatch(/Applies in event mode too/)
    expect(help?.textContent).toMatch(/skipped/)
    expect(screen.getByText(/Each notified object under the prefix that matches the file pattern/)).toBeTruthy()
  })

  it('describes the pattern for poll mode without the event note', () => {
    mount({ type: 'cloud_storage', cloud_storage: { mode: 'poll', poll_interval_seconds: 60 } })
    const help = document.getElementById(patternInput()!.getAttribute('aria-describedby')!)
    expect(help?.textContent).toMatch(/file name/)
    expect(help?.textContent).not.toMatch(/event mode/)
    expect(screen.getByText(/The bucket is polled under the prefix/)).toBeTruthy()
  })

  it('keeps a poll-mode pattern visible after switching to event mode', () => {
    const { latest } = mount({ type: 'cloud_storage', cloud_storage: { file_pattern: '*.csv', poll_interval_seconds: 60 } })
    expect(patternInput()!.value).toBe('*.csv')
    fireEvent.change(modeSelect(), { target: { value: 'event' } })
    expect(csOf(latest)!.mode).toBe('event')
    expect(patternInput()!.value).toBe('*.csv')
    expect(csOf(latest)!.file_pattern).toBe('*.csv')
  })

  it('lets the pattern be edited or cleared in event mode', () => {
    const { latest } = mount({ type: 'cloud_storage', cloud_storage: { mode: 'event', file_pattern: '*.csv' } })
    fireEvent.change(patternInput()!, { target: { value: '*.json' } })
    expect(csOf(latest)!.file_pattern).toBe('*.json')
    fireEvent.change(patternInput()!, { target: { value: '' } })
    expect(csOf(latest)!.file_pattern).toBe('')
    fireEvent.change(prefixInput(), { target: { value: 'incoming/' } })
    expect(csOf(latest)!.prefix).toBe('incoming/')
  })

  it('has no file pattern on a destination', () => {
    mount({ type: 'cloud_storage', cloud_storage: { bucket: 'b' } }, 'producer')
    expect(patternInput()).toBeNull()
  })
})
