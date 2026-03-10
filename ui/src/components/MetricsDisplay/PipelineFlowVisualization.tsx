/**
 * PipelineFlowVisualization Component
 * Display data pipeline flow with component statuses
 */

import type { ConnectionMetrics } from '@/types/models'

interface PipelineFlowVisualizationProps {
  metrics: ConnectionMetrics | null | undefined
}

const statusColors: Record<string, { bg: string; text: string; border: string }> = {
  active: {
    bg: 'bg-green-50',
    text: 'text-green-800',
    border: 'border-green-300',
  },
  idle: {
    bg: 'bg-gray-50',
    text: 'text-gray-800',
    border: 'border-gray-300',
  },
  error: {
    bg: 'bg-red-50',
    text: 'text-red-800',
    border: 'border-red-300',
  },
}

export function PipelineFlowVisualization({ metrics }: PipelineFlowVisualizationProps) {
  if (!metrics) {
    return (
      <div className="p-4 rounded-lg border border-gray-200 bg-gray-50 text-center">
        <p className="text-gray-600 text-sm">No pipeline data available</p>
      </div>
    )
  }

  const components = [
    {
      name: 'Consumer',
      status: metrics.components.consumer.status,
      processed: metrics.components.consumer.messages_processed,
      errors: metrics.components.consumer.errors,
    },
    {
      name: 'Converter',
      status: metrics.components.converter.status,
      processed: metrics.components.converter.messages_processed,
      errors: metrics.components.converter.errors,
    },
    {
      name: 'Filter',
      status: metrics.components.filter.status,
      processed: metrics.components.filter.messages_processed,
      errors: metrics.components.filter.errors,
    },
    {
      name: 'Producer',
      status: metrics.components.producer.status,
      processed: metrics.components.producer.messages_sent,
      errors: metrics.components.producer.errors,
    },
  ]

  return (
    <div className="p-4 rounded-lg border border-gray-200 bg-white">
      <h3 className="text-sm font-bold text-gray-900 mb-4">Pipeline Flow</h3>

      {/* SVG Flow Diagram */}
      <div className="overflow-x-auto mb-4">
        <svg width="100%" height="120" viewBox="0 0 600 120" preserveAspectRatio="xMidYMid meet" style={{ maxWidth: '100%', display: 'block' }}>
          {/* Arrows and components */}
          {components.map((component, index) => {
            const x = 50 + index * 140

            return (
              <g key={component.name}>
                {/* Arrow from previous component */}
                {index > 0 && (
                  <>
                    <line x1={x - 50} y1="60" x2={x - 20} y2="60" stroke="#cbd5e1" strokeWidth="2" />
                    <polygon points={`${x - 15},60 ${x - 23},56 ${x - 23},64`} fill="#cbd5e1" />
                  </>
                )}

                {/* Component box */}
                <rect
                  x={x}
                  y="30"
                  width="100"
                  height="60"
                  rx="4"
                  fill="white"
                  stroke="#cbd5e1"
                  strokeWidth="2"
                />

                {/* Status indicator */}
                <circle
                  cx={x + 90}
                  cy="35"
                  r="5"
                  fill={component.status === 'active' ? '#22c55e' : component.status === 'error' ? '#ef4444' : '#9ca3af'}
                />

                {/* Component name */}
                <text x={x + 50} y="50" fontSize="13" fontWeight="600" fill="#1f2937" textAnchor="middle">
                  {component.name}
                </text>

                {/* Stats */}
                <text x={x + 50} y="70" fontSize="11" fill="#6b7280" textAnchor="middle">
                  {component.processed} msgs
                </text>
                {component.errors > 0 && (
                  <text x={x + 50} y="85" fontSize="11" fill="#ef4444" fontWeight="600" textAnchor="middle">
                    {component.errors} errors
                  </text>
                )}
              </g>
            )
          })}
        </svg>
      </div>

      {/* Component Status Grid */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
        {components.map((component) => {
          const componentColors = statusColors[component.status]
          return (
            <div key={component.name} className={`p-3 rounded border ${componentColors.bg} ${componentColors.border}`}>
              <div className="flex items-center justify-between mb-2">
                <p className={`text-xs font-semibold ${componentColors.text}`}>{component.name}</p>
                <span
                  className={`w-2 h-2 rounded-full ${
                    component.status === 'active'
                      ? 'bg-green-500'
                      : component.status === 'error'
                        ? 'bg-red-500'
                        : 'bg-gray-500'
                  }`}
                />
              </div>
              <p className="text-xs text-gray-600">
                {component.status.charAt(0).toUpperCase() + component.status.slice(1)}
              </p>
            </div>
          )
        })}
      </div>

      {/* Overall Pipeline Metrics */}
      <div className="mt-4 grid grid-cols-3 gap-4">
        <div className="p-3 bg-blue-50 rounded border border-blue-200">
          <p className="text-xs text-gray-600">Messages In</p>
          <p className="text-lg font-bold text-blue-900">{metrics.total_messages_in}</p>
        </div>
        <div className="p-3 bg-green-50 rounded border border-green-200">
          <p className="text-xs text-gray-600">Messages Out</p>
          <p className="text-lg font-bold text-green-900">{metrics.total_messages_out}</p>
        </div>
        <div className={`p-3 rounded border ${metrics.total_errors > 0 ? 'bg-red-50 border-red-200' : 'bg-gray-50 border-gray-200'}`}>
          <p className="text-xs text-gray-600">Total Errors</p>
          <p className={`text-lg font-bold ${metrics.total_errors > 0 ? 'text-red-900' : 'text-gray-900'}`}>
            {metrics.total_errors}
          </p>
        </div>
      </div>
    </div>
  )
}
