export interface Node {
  id: string
  type: 'input' | 'filter' | 'converter' | 'output'
  data: {
    label: string
    config?: Record<string, unknown>
    type?: string
  }
  position: { x: number; y: number }
}

export interface Edge {
  id?: string
  source: string
  target: string
}

export interface NodeData {
  label: string
  config?: Record<string, unknown>
  type?: string
}

export interface Canvas {
  id: string
  name: string
  nodes: Node[]
  edges: Edge[]
  deployedConnectionId?: string
  /** The name the server has for deployedConnectionId, when known; a deploy
   *  compares the tab name with it to tell a rename (deployIdentity). */
  deployedName?: string
  createdAt: number
  updatedAt: number
}
