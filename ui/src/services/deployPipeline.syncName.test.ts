/**
 * #502: a linked canvas's name once the server's current name is known.
 *
 * deployedName records the server's name, and Deploy sends a name only when
 * the tab differs from it. When the name changed elsewhere, the tab and
 * deployedName still agreed, so Deploy sent nothing and the tab kept a name
 * the server no longer had. syncWithServerName brings deployedName in line
 * and moves the tab along unless the user renamed it here.
 */

import { describe, it, expect } from 'vitest'
import { syncWithServerName, deployIdentity, deployPlan, type CanvasIdentity } from './deployPipeline'

const now = new Date('2026-10-29T09:00:00Z')
const linked = (name: string, deployedName?: string): CanvasIdentity =>
  ({ name, deployedConnectionId: 'c1', ...(deployedName !== undefined && { deployedName }) })

describe('syncWithServerName', () => {
  it.each([
    ['an edit tab never renamed follows the server name, prefix kept',
      linked('Edit: Old name', 'Old name'), 'New name', 'Edit: New name'],
    ['a plain tab never renamed follows the server name',
      linked('Old name', 'Old name'), 'New name', 'New name'],
    ['a tab with stray spaces around the old name still counts as not renamed',
      linked('Edit:  Old name ', 'Old name'), 'New name', 'Edit: New name'],
    ['a default "Tenant N" tab follows the server name',
      linked('Tenant 3', 'Old name'), 'New name', 'New name'],
    ['a blank edit tab follows the server name, prefix kept',
      linked('Edit: ', 'Old name'), 'New name', 'Edit: New name'],
    ['an edit tab renamed here keeps its name',
      linked('Edit: Mine', 'Old name'), 'New name', 'Edit: Mine'],
    ['a plain tab renamed here keeps its name',
      linked('Mine', 'Old name'), 'New name', 'Mine'],
    ['a tab with no recorded deployedName is kept',
      linked('Edit: Old name'), 'New name', 'Edit: Old name'],
    ['a tab already matching the server is unchanged',
      linked('Edit: Same', 'Same'), 'Same', 'Edit: Same'],
  ])('%s', (_, canvas, serverName, wantName) => {
    expect(syncWithServerName(canvas, serverName)).toEqual({ name: wantName, deployedName: serverName })
  })

  it('deployedName always becomes the server name', () => {
    const canvases = [
      linked('Edit: Old name', 'Old name'), linked('Mine', 'Old name'), linked('Tenant 3', 'Old name'),
      linked('Edit: Old name'), linked('', 'Old name'),
    ]
    for (const canvas of canvases) {
      expect(syncWithServerName(canvas, 'New name').deployedName).toBe('New name')
    }
  })
})

// The point of the sync: what the next Deploy sends.
describe('Deploy after a sync with the server name', () => {
  const synced = (canvas: CanvasIdentity, serverName: string): CanvasIdentity =>
    ({ ...canvas, ...syncWithServerName(canvas, serverName) })

  it('with no local rename, an update sends no name', () => {
    for (const before of [linked('Edit: Old name', 'Old name'), linked('Old name', 'Old name'), linked('Tenant 3', 'Old name')]) {
      const after = synced(before, 'New name')
      expect(deployIdentity(after, now)).toEqual({})
      expect(deployPlan(after, false, now)).toEqual({ previousConnectionId: 'c1', identity: {} })
    }
  })

  it('with a local rename, the update sends the tab name', () => {
    const after = synced(linked('Edit: Mine', 'Old name'), 'New name')
    expect(deployIdentity(after, now)).toEqual({ name: 'Mine' })
    expect(deployPlan(after, false, now)).toEqual({ previousConnectionId: 'c1', identity: { name: 'Mine' } })
  })

  it('a tab still reading the old name does not revert the rename made elsewhere', () => {
    // Without the sync, the next Deploy after a rename elsewhere kept the
    // stale tab; with it, the tab shows the server name and nothing is sent.
    const after = synced(linked('Edit: Old name', 'Old name'), 'New name')
    expect(after.name).toBe('Edit: New name')
    expect(deployPlan(after, false, now).identity).not.toHaveProperty('name')
  })

  it('a tab with no recorded deployedName sends its name, as before the sync', () => {
    const before = linked('Edit: Webhook in')
    expect(deployIdentity(before, now)).toEqual({ name: 'Webhook in' })
    const after = synced(before, 'Webhook in v2')
    expect(deployIdentity(after, now)).toEqual({ name: 'Webhook in' })
  })
})
