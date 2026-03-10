export interface Node {
  id: string
  type: 'consumer' | 'filter' | 'converter' | 'producer'
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
