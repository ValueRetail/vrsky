/**
 * MetricsChart Component
 * Display metrics as a simple bar chart using SVG
 */

interface DataPoint {
  label: string
  value: number
  color?: string
}

interface MetricsChartProps {
  data: DataPoint[]
  title?: string
  height?: number
  maxValue?: number
  showValues?: boolean
}

export function MetricsChart({
  data,
  title,
  height = 200,
  maxValue,
  showValues = true,
}: MetricsChartProps) {
  if (!data || data.length === 0) {
    return (
      <div className="p-4 rounded-lg border border-gray-200 bg-gray-50 text-center">
        <p className="text-gray-600 text-sm">No data available</p>
      </div>
    )
  }

  // Calculate max value for scaling
  const max = maxValue || Math.max(...data.map((d) => d.value), 1)
  const padding = 40
  const chartHeight = height - padding
  const barWidth = Math.max(30, (100 / (data.length * 1.5)))
  const chartWidth = padding + data.length * (barWidth + 20)

  return (
    <div className="p-4 rounded-lg border border-gray-200 bg-white">
      {title && <h3 className="text-sm font-bold text-gray-900 mb-3">{title}</h3>}
      <div className="overflow-x-auto">
        <svg width={chartWidth} height={height} viewBox={`0 0 ${chartWidth} ${height}`}>
          {/* Grid lines */}
          {[0, 0.25, 0.5, 0.75, 1].map((percentage) => {
            const y = height - padding + (percentage * -chartHeight)
            const value = Math.round(max * percentage)
            return (
              <g key={`grid-${percentage}`}>
                <line x1={padding} y1={y} x2={chartWidth} y2={y} stroke="#e5e7eb" strokeWidth="1" />
                <text x={5} y={y + 4} fontSize="11" fill="#6b7280" textAnchor="end">
                  {value}
                </text>
              </g>
            )
          })}

          {/* Bars */}
          {data.map((item, index) => {
            const x = padding + index * (barWidth + 20) + 10
            const barHeight = (item.value / max) * chartHeight
            const y = height - padding - barHeight
            const barColor = item.color || '#3b82f6'

            return (
              <g key={`bar-${index}`}>
                <rect x={x} y={y} width={barWidth} height={barHeight} fill={barColor} rx="2" />
                {showValues && item.value > 0 && (
                  <text
                    x={x + barWidth / 2}
                    y={y - 5}
                    fontSize="12"
                    fill="#374151"
                    textAnchor="middle"
                    fontWeight="600"
                  >
                    {item.value}
                  </text>
                )}
                <text
                  x={x + barWidth / 2}
                  y={height - 5}
                  fontSize="11"
                  fill="#6b7280"
                  textAnchor="middle"
                  className="max-w-12 truncate"
                >
                  {item.label}
                </text>
              </g>
            )
          })}
        </svg>
      </div>
    </div>
  )
}
