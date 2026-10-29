/**
 * The SFTP and cloud-storage source panels show a poll interval of 60. The
 * consumers read a missing field as 0, "poll once", so a node saved without
 * touching the field must carry the 60 it showed (#589).
 */

import { describe, it, expect, vi } from 'vitest'
import { useState } from 'react'
import { render, screen, fireEvent } from '@testing-library/react'
import SFTPConsumerConfig from './SFTPConsumerConfig'
import CloudStorageConfigEditor from './CloudStorageConfigEditor'

type Cfg = Record<string, unknown>
type Block = { poll_interval_seconds?: number; host?: string; bucket?: string } | undefined

// Renders a panel the way PropertyEditor does: the host owns the config state
// and hands the panel setConfig. `latest` is the last config the panel set (the
// first one if it set none), which is what Save Configuration would pass on.
function mount(config: Cfg, role: 'consumer' | 'producer' | 'sftp' = 'sftp') {
  const latest = { current: config }
  function Host({ initial }: { initial: Cfg }) {
    const [c, setC] = useState<Cfg>(initial)
    const setConfig = (next: Cfg) => { latest.current = next; setC(next) }
    return role === 'sftp'
      ? <SFTPConsumerConfig config={c} setConfig={setConfig} />
      : <CloudStorageConfigEditor config={c} setConfig={setConfig} role={role} />
  }
  render(<Host initial={config} />)
  return { latest }
}

const pollInput = () => screen.getByLabelText('Poll interval (s, 0 = once)') as HTMLInputElement
const sftpOf = (l: { current: Cfg }) => l.current.sftp as Block
const csOf = (l: { current: Cfg }) => l.current.cloud_storage as Block

describe('SFTP source poll interval', () => {
  it('saves the 60 it shows when the field is never touched', () => {
    const { latest } = mount({ type: 'sftp', sftp: { host: 'h' } })
    expect(pollInput().value).toBe('60')
    expect(sftpOf(latest)!.poll_interval_seconds).toBe(60)
    expect(sftpOf(latest)!.host).toBe('h')
  })

  it('saves it for a node whose sftp block does not exist yet', () => {
    const { latest } = mount({ type: 'sftp' })
    expect(sftpOf(latest)!.poll_interval_seconds).toBe(60)
  })

  it.each([0, 15, 600])('does not overwrite an explicit %i', (n) => {
    const { latest } = mount({ type: 'sftp', sftp: { poll_interval_seconds: n } })
    expect(sftpOf(latest)!.poll_interval_seconds).toBe(n)
    expect(pollInput().value).toBe(String(n))
  })

  it('keeps an interval the user types, and lets them choose 0 for one poll', () => {
    const { latest } = mount({ type: 'sftp', sftp: {} })
    fireEvent.change(pollInput(), { target: { value: '120' } })
    expect(sftpOf(latest)!.poll_interval_seconds).toBe(120)
    fireEvent.change(pollInput(), { target: { value: '0' } })
    expect(sftpOf(latest)!.poll_interval_seconds).toBe(0)
  })

  it('fills it in when the panel stays mounted and the config changes to one without it', () => {
    // PropertyEditor does not remount the panel when the selection moves to
    // another SFTP node: the config under it is swapped.
    const setConfig = vi.fn()
    const { rerender } = render(
      <SFTPConsumerConfig config={{ type: 'sftp', sftp: { poll_interval_seconds: 30 } }} setConfig={setConfig} />,
    )
    expect(setConfig).not.toHaveBeenCalled()
    rerender(<SFTPConsumerConfig config={{ type: 'sftp', sftp: {} }} setConfig={setConfig} />)
    expect(setConfig).toHaveBeenCalledWith({ type: 'sftp', sftp: { poll_interval_seconds: 60 } })
  })
})

describe('cloud-storage source poll interval', () => {
  it('saves the 60 it shows when the field is never touched', () => {
    const { latest } = mount({ type: 'cloud_storage', cloud_storage: { provider: 's3', bucket: 'b' } }, 'consumer')
    expect(pollInput().value).toBe('60')
    expect(csOf(latest)!.poll_interval_seconds).toBe(60)
    expect(csOf(latest)!.bucket).toBe('b')
  })

  it('saves it for a node whose cloud_storage block does not exist yet', () => {
    const { latest } = mount({ type: 'cloud_storage' }, 'consumer')
    expect(csOf(latest)!.poll_interval_seconds).toBe(60)
  })

  it.each([0, 15, 600])('does not overwrite an explicit %i', (n) => {
    const { latest } = mount({ type: 'cloud_storage', cloud_storage: { poll_interval_seconds: n } }, 'consumer')
    expect(csOf(latest)!.poll_interval_seconds).toBe(n)
  })

  it('does not add an interval to an event-mode node', () => {
    const { latest } = mount({ type: 'cloud_storage', cloud_storage: { mode: 'event' } }, 'consumer')
    expect(csOf(latest)!.poll_interval_seconds).toBeUndefined()
  })

  it('adds it when an event-mode node is switched back to poll', () => {
    const { latest } = mount({ type: 'cloud_storage', cloud_storage: { mode: 'event' } }, 'consumer')
    const modeSelect = Array.from(document.querySelectorAll('select'))
      .find((s) => Array.from(s.options).some((o) => o.value === 'event')) as HTMLSelectElement
    fireEvent.change(modeSelect, { target: { value: 'poll' } })
    expect(pollInput().value).toBe('60')
    expect(csOf(latest)!.poll_interval_seconds).toBe(60)
  })

  it('does not add an interval to a cloud-storage destination', () => {
    const { latest } = mount({ type: 'cloud_storage', cloud_storage: { bucket: 'b' } }, 'producer')
    expect(csOf(latest)!.poll_interval_seconds).toBeUndefined()
  })
})
