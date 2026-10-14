import { useState } from 'react'
import type { Node } from '../../types/pipeline'

/**
 * The property editor's local state for the selected node: its config and
 * label, plus whether the label is being edited. A re-render of the same node
 * keeps the local state (unsaved edits); selecting a different node loads
 * that node's config. The state follows the node id, adjusted during render
 * (React's pattern for state that depends on a prop); an effect that did the
 * same caused a cascading render.
 */
export function useNodeEditorState(node: Node) {
  const [config, setConfig] = useState<Record<string, unknown>>(node.data.config || {})
  const [labelValue, setLabelValue] = useState(node.data.label || '')
  const [editingLabel, setEditingLabel] = useState(false)
  const [editedNodeId, setEditedNodeId] = useState(node.id)
  if (editedNodeId !== node.id) {
    setEditedNodeId(node.id)
    setConfig(node.data.config || {})
    setLabelValue(node.data.label || '')
    setEditingLabel(false)
  }
  return { config, setConfig, labelValue, setLabelValue, editingLabel, setEditingLabel }
}
