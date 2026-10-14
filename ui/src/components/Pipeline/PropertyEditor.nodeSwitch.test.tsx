/**
 * The node editor keeps unsaved edits while the same node re-renders and
 * loads the other node's config when the selection changes. The state
 * follows the node id, adjusted during render; the effect that used to do
 * this caused a cascading render (lint: react-hooks/set-state-in-effect).
 */

import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import PropertyEditor from './PropertyEditor'
import type { Node } from '../../types/pipeline'

const fileNode = (id: string, path: string): Node => ({
  id, type: 'input', position: { x: 0, y: 0 },
  data: { label: id, config: { type: 'file', file: { path } } },
})
const props = { onUpdate: vi.fn(), onClose: vi.fn() }
const field = () => screen.getByLabelText('Watch Directory') as HTMLInputElement

describe('PropertyEditor node switching', () => {
  it('keeps an unsaved edit across a re-render of the same node', () => {
    const a = fileNode('a', '/a')
    const { rerender } = render(<PropertyEditor node={a} {...props} />)
    expect(field().value).toBe('/a')
    fireEvent.change(field(), { target: { value: '/a-edited' } })
    rerender(<PropertyEditor node={fileNode('a', '/a')} {...props} />) // same id, fresh object
    expect(field().value).toBe('/a-edited')
  })

  it("loads the other node's config on switch and drops the unsaved edit", () => {
    const a = fileNode('a', '/a')
    const { rerender } = render(<PropertyEditor node={a} {...props} />)
    fireEvent.change(field(), { target: { value: '/a-edited' } })
    rerender(<PropertyEditor node={fileNode('b', '/b')} {...props} />)
    expect(field().value).toBe('/b')
    rerender(<PropertyEditor node={a} {...props} />)
    expect(field().value).toBe('/a')
  })
})
