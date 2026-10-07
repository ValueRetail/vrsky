/**
 * A 402 shows one toast per quiet period, not one per failed request.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'

const addNotification = vi.fn()
vi.mock('@/store/uiStore', () => ({
  useUIStore: { getState: () => ({ addNotification }) },
}))

import { reportPlanRequired, resetPlanRequired } from './planRequired'

beforeEach(() => {
  addNotification.mockReset()
  resetPlanRequired()
})

describe('reportPlanRequired', () => {
  it('toasts once, then stays quiet for a while, then toasts again', () => {
    expect(reportPlanRequired(1_000_000)).toBe(true)
    expect(reportPlanRequired(1_000_500)).toBe(false)
    expect(reportPlanRequired(1_010_000)).toBe(false)
    expect(reportPlanRequired(1_016_000)).toBe(true)
    expect(addNotification).toHaveBeenCalledTimes(2)
    expect(addNotification.mock.calls[0][0]).toMatchObject({ type: 'warning', title: 'Your workspace needs a plan' })
    expect(addNotification.mock.calls[0][0].message).toMatch(/Settings → Plan/)
  })
})
