/**
 * PipelineFlowVisualization Component
 * Renders the connection's ACTUAL pipeline (its nodes/edges) with per-component
 * live metrics — previously it always drew a fixed Consumer→Converter→Filter→
 * Producer chain regardless of the real graph (#129).
 */

import type {
  ConnectionMetrics,
  ConnectionNode,
  ConnectionEdge,
  ConnectionStatus,
} from '@/types/models'

interface PipelineFlowVisualizationProps {
  metrics: ConnectionMetrics | null | undefined
  nodes?: ConnectionNode[]
  edges?: ConnectionEdge[]
  /** Connection status, so an idle pipeline reads "running, no traffic yet". */
  status?: ConnectionStatus
}

type MetricKey = 'consumer' | 'converter' | 'filter' | 'producer'

const statusColors: Record<string, { bg: string; text: string; border: string }> = {
  active: { bg: 'bg-green-50', text: 'text-green-800', border: 'border-green-300' },
  idle: { bg: 'bg-gray-50', text: 'text-gray-800', border: 'border-gray-300' },
  error: { bg: 'bg-red-50', text: 'text-red-800', border: 'border-red-300' },
}

// API node type → the metrics bucket it reports under. The metrics model has a
// single bucket per type, so multiple nodes of one type share its counters.
const NODE_TYPE_TO_METRIC: Record<string, MetricKey> = {
  consumer: 'consumer',
  converter: 'converter',
  filter: 'filter',
  producer: 'producer',
}

// Order nodes left→right by longest-path depth from a source (matches data
// flow); falls back to input order when there are no edges.
function orderNodes(nodes: ConnectionNode[], edges: ConnectionEdge[]): ConnectionNode[] {
  if (edges.length === 0) return nodes
  const adj = new Map<string, string[]>()
  const indeg = new Map<string, number>()
  nodes.forEach((n) => indeg.set(n.id, 0))
  edges.forEach((e) => {
    adj.set(e.source, [...(adj.get(e.source) ?? []), e.target])
    indeg.set(e.target, (indeg.get(e.target) ?? 0) + 1)
  })
  const depth = new Map<string, number>()
  const queue = nodes.filter((n) => (indeg.get(n.id) ?? 0) === 0).map((n) => n.id)
  queue.forEach((id) => depth.set(id, 0))
  const remaining = new Map(indeg)
  while (queue.length) {
    const id = queue.shift() as string
    const d = depth.get(id) ?? 0
    for (const next of adj.get(id) ?? []) {
      depth.set(next, Math.max(depth.get(next) ?? 0, d + 1))
      const left = (remaining.get(next) ?? 0) - 1
      remaining.set(next, left)
      if (left === 0) queue.push(next)
    }
  }
  return [...nodes].sort((a, b) => (depth.get(a.id) ?? 0) - (depth.get(b.id) ?? 0))
}

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)

export function PipelineFlowVisualization({ metrics, nodes, edges, status }: PipelineFlowVisualizationProps) {
  const graphNodes = nodes ?? []

  if (graphNodes.length === 0) {
    return (
      <div className="p-4 rounded-lg border border-gray-200 bg-gray-50 text-center">
        <p className="text-gray-600 text-sm">No pipeline defined for this connection.</p>
      </div>
    )
  }

  const ordered = orderNodes(graphNodes, edges ?? [])

  const components = ordered.map((node) => {
    const metricKey = NODE_TYPE_TO_METRIC[node.type] ?? 'consumer'
    const comp = metrics?.components?.[metricKey]
    const connector = typeof node.config?.type === 'string' ? (node.config.type as string) : undefined
    const processed =
      metricKey === 'producer'
        ? (comp as { messages_sent?: number } | undefined)?.messages_sent ?? comp?.messages_processed ?? 0
        : comp?.messages_processed ?? 0
    return {
      id: node.id,
      name: cap(node.type),
      connector,
      status: comp?.status ?? 'idle',
      processed,
      errors: comp?.errors ?? 0,
      hasMetrics: !!comp,
    }
  })

  // Idle running pipeline → reassure rather than imply something is broken.
  const anyTraffic = components.some((c) => c.processed > 0 || c.errors > 0)
  const idleHint =
    !!metrics && !anyTraffic && status === 'running'
      ? 'Running — no traffic yet.'
      : !metrics && status === 'running'
        ? 'Running — waiting for metrics…'
        : null

  const BOX_W = 100
  const GAP = 40
  const STEP = BOX_W + GAP
  const width = Math.max(600, components.length * STEP + 40)

  return (
    <div className="p-4 rounded-lg border border-gray-200 bg-white">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-sm font-bold text-gray-900">Pipeline Flow</h3>
        {idleHint && <span className="text-xs text-gray-500">{idleHint}</span>}
      </div>

      <div className="overflow-x-auto mb-4">
        <svg width="100%" height="130" viewBox={`0 0 ${width} 130`} preserveAspectRatio="xMidYMid meet" style={{ maxWidth: '100%', display: 'block' }}>
          {components.map((component, index) => {
            const x = 30 + index * STEP
            return (
              <g key={component.id}>
                {index > 0 && (
                  <>
                    <line x1={x - GAP} y1="60" x2={x - 18} y2="60" stroke="#cbd5e1" strokeWidth="2" />
                    <polygon points={`${x - 13},60 ${x - 21},56 ${x - 21},64`} fill="#cbd5e1" />
                  </>
                )}
                <rect x={x} y="30" width={BOX_W} height="64" rx="4" fill="white" stroke="#cbd5e1" strokeWidth="2" />
                <circle
                  cx={x + BOX_W - 10}
                  cy="35"
                  r="5"
                  fill={component.status === 'active' ? '#22c55e' : component.status === 'error' ? '#ef4444' : '#9ca3af'}
                />
                <text x={x + BOX_W / 2} y="50" fontSize="13" fontWeight="600" fill="#1f2937" textAnchor="middle">
                  {component.name}
                </text>
                {component.connector && (
                  <text x={x + BOX_W / 2} y="64" fontSize="10" fill="#9ca3af" textAnchor="middle">
                    {component.connector}
                  </text>
                )}
                <text x={x + BOX_W / 2} y="80" fontSize="11" fill="#6b7280" textAnchor="middle">
                  {component.hasMetrics ? `${component.processed} msgs` : '—'}
                </text>
                {component.errors > 0 && (
                  <text x={x + BOX_W / 2} y="92" fontSize="10" fill="#ef4444" fontWeight="600" textAnchor="middle">
                    {component.errors} errors
                  </text>
                )}
              </g>
            )
          })}
        </svg>
      </div>

      <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
        {components.map((component) => {
          const c = statusColors[component.status] ?? statusColors.idle
          return (
            <div key={component.id} className={`p-3 rounded border ${c.bg} ${c.border}`}>
              <div className="flex items-center justify-between mb-2">
                <p className={`text-xs font-semibold ${c.text}`}>{component.name}</p>
                <span
                  className={`w-2 h-2 rounded-full ${
                    component.status === 'active' ? 'bg-green-500' : component.status === 'error' ? 'bg-red-500' : 'bg-gray-400'
                  }`}
                />
              </div>
              <p className="text-xs text-gray-600">{component.hasMetrics ? `${component.processed} processed` : 'no data'}</p>
              {component.errors > 0 && <p className="text-xs text-red-600 font-semibold">{component.errors} errors</p>}
            </div>
          )
        })}
      </div>
    </div>
  )
}
