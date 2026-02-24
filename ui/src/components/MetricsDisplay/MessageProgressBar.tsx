/**
 * MessageProgressBar Component
 * Display throughput and message flow as an animated progress indicator
 */

interface MessageProgressBarProps {
  messagesIn: number
  messagesOut: number
  throughputMps: number
  title?: string
}

export function MessageProgressBar({
  messagesIn,
  messagesOut,
  throughputMps,
  title = 'Throughput',
}: MessageProgressBarProps) {
  // Calculate progress percentage (out of in)
  const progress = messagesIn > 0 ? (messagesOut / messagesIn) * 100 : 0
  const progressCapped = Math.min(progress, 100)

  return (
    <div className="p-4 rounded-lg border border-gray-200 bg-white">
      <div className="flex items-center justify-between mb-3">
        <h3 className="text-sm font-bold text-gray-900">{title}</h3>
        <span className="text-xs font-semibold text-blue-600">{throughputMps.toFixed(2)} msgs/sec</span>
      </div>

      {/* Input to Output Progress */}
      <div className="mb-4">
        <div className="flex items-center justify-between text-xs text-gray-600 mb-1">
          <span>Input</span>
          <span>Output</span>
        </div>
        <div className="relative h-2 bg-gray-100 rounded-full overflow-hidden">
          <div
            className="h-full bg-gradient-to-r from-blue-400 via-blue-600 to-blue-700 rounded-full transition-all duration-300"
            style={{ width: `${progressCapped}%` }}
          />
        </div>
        <div className="flex items-center justify-between text-xs text-gray-600 mt-2">
          <span className="font-semibold">{messagesIn}</span>
          <span>{progressCapped.toFixed(1)}%</span>
          <span className="font-semibold">{messagesOut}</span>
        </div>
      </div>

      {/* Status Indicator */}
      <div className="flex items-center gap-2">
        <div className={`w-3 h-3 rounded-full ${throughputMps > 0 ? 'bg-green-500' : 'bg-gray-300'} animate-pulse`} />
        <span className="text-xs text-gray-600">
          {throughputMps > 0 ? 'Actively processing' : 'No throughput'}
        </span>
      </div>

      {/* Efficiency Metric */}
      <div className="mt-3 p-2 bg-blue-50 rounded border border-blue-100">
        <p className="text-xs text-blue-900 font-medium">
          {progress >= 99 ? 'Excellent efficiency' : progress >= 90 ? 'Good efficiency' : progress >= 75 ? 'Fair efficiency' : 'Processing...'}
        </p>
      </div>
    </div>
  )
}
