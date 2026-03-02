/**
 * MetricsCard Component
 * Display a single metric value with label, unit, and optional trend indicator
 */

interface MetricsCardProps {
  label: string
  value: number | string
  unit?: string
  trend?: 'up' | 'down' | 'stable'
  trendValue?: number | string
  icon?: React.ReactNode
  color?: 'blue' | 'green' | 'red' | 'yellow' | 'purple'
  loading?: boolean
}

const colorStyles: Record<string, string> = {
  blue: 'bg-blue-50 border-blue-200 text-blue-700',
  green: 'bg-green-50 border-green-200 text-green-700',
  red: 'bg-red-50 border-red-200 text-red-700',
  yellow: 'bg-yellow-50 border-yellow-200 text-yellow-700',
  purple: 'bg-purple-50 border-purple-200 text-purple-700',
}

const trendColors: Record<string, string> = {
  up: 'text-green-600',
  down: 'text-red-600',
  stable: 'text-gray-600',
}

const trendIcons: Record<string, string> = {
  up: '↑',
  down: '↓',
  stable: '→',
}

export function MetricsCard({
  label,
  value,
  unit = '',
  trend,
  trendValue,
  icon,
  color = 'blue',
  loading = false,
}: MetricsCardProps) {
  if (loading) {
    return (
      <div className={`p-4 rounded-lg border border-gray-200 ${colorStyles[color]}`}>
        <div className="flex items-center justify-between mb-3">
          <span className="text-sm font-medium text-gray-600">{label}</span>
          {icon && <span className="text-xl">{icon}</span>}
        </div>
        <div className="animate-pulse">
          <div className="h-8 bg-gray-300 rounded w-2/3"></div>
        </div>
      </div>
    )
  }

  return (
    <div className={`p-4 rounded-lg border border-gray-200 ${colorStyles[color]}`}>
      <div className="flex items-center justify-between mb-3">
        <span className="text-sm font-medium text-gray-600">{label}</span>
        {icon && <span className="text-xl">{icon}</span>}
      </div>
      <div className="flex items-baseline justify-between">
        <div>
          <span className="text-2xl font-bold">{value}</span>
          {unit && <span className="text-sm ml-1">{unit}</span>}
        </div>
        {trend && (
          <div className={`text-sm font-medium flex items-center gap-1 ${trendColors[trend]}`}>
            <span>{trendIcons[trend]}</span>
            {trendValue && <span>{trendValue}</span>}
          </div>
        )}
      </div>
    </div>
  )
}
