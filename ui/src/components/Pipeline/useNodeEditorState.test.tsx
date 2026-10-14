/**
 * The node editor keeps unsaved edits while the same node re-renders and
 * loads the other node's config when the selection changes.
 */

import { describe, it, expect } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { useNodeEditorState } from './useNodeEditorState'
import type { Node } from '../../types/pipeline'

const fileNode = (id: string, path: string): Node => ({
  id, type: 'input', position: { x: 0, y: 0 },
  data: { label: id, config: { type: 'file', file: { path } } },
})

// A host as small as the property editor's own use of the hook.
function Host({ node }: { node: Node }) {
  const { config, setConfig, labelValue, editingLabel, setEditingLabel } = useNodeEditorState(node)
  const path = ((config.file as { path?: string } | undefined)?.path) || ''
  return (
    <>
      <label htmlFor="path">Path</label>
      <input id="path" value={path} onChange={(e) => setConfig({ ...config, file: { path: e.target.value } })} />
      <output data-testid="label">{labelValue}{editingLabel ? ' (editing)' : ''}</output>
      <button onClick={() => setEditingLabel(true)}>edit label</button>
    </>
  )
}
const field = () => screen.getByLabelText('Path') as HTMLInputElement

describe('useNodeEditorState', () => {
  it('keeps an unsaved edit across a re-render of the same node', () => {
    const { rerender } = render(<Host node={fileNode('a', '/a')} />)
    expect(field().value).toBe('/a')
    fireEvent.change(field(), { target: { value: '/a-edited' } })
    rerender(<Host node={fileNode('a', '/a')} />) // same id, fresh object
    expect(field().value).toBe('/a-edited')
  })

  it("loads the other node's config on switch, drops the edit, and stops editing the label", () => {
    const a = fileNode('a', '/a')
    const { rerender } = render(<Host node={a} />)
    fireEvent.change(field(), { target: { value: '/a-edited' } })
    fireEvent.click(screen.getByText('edit label'))
    expect(screen.getByTestId('label')).toHaveTextContent('a (editing)')
    rerender(<Host node={fileNode('b', '/b')} />)
    expect(field().value).toBe('/b')
    expect(screen.getByTestId('label')).toHaveTextContent(/^b$/)
    rerender(<Host node={a} />)
    expect(field().value).toBe('/a')
  })
})
