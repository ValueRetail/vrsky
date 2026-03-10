/**
 * AutoGeneratorControls Component
 * Controls for starting/stopping automated test message generation
 */

import { useEffect, useState } from 'react'
import { testDataService } from '@/services/testDataService'
import { useUIStore } from '@/store/uiStore'
import { isAPIError, getErrorMessage } from '@/utils/errors'
import type { AutoGeneratorStatusResponse } from '@/types/api'

interface AutoGeneratorControlsProps {
  connectionId: string
  onStatusChange?: (status: AutoGeneratorStatusResponse) => void
}

export function AutoGeneratorControls({ connectionId, onStatusChange }: AutoGeneratorControlsProps) {
  const [status, setStatus] = useState<AutoGeneratorStatusResponse | null>(null)
  const [loading, setLoading] = useState(false)
  const [rate, setRate] = useState(1)
  const { addNotification } = useUIStore()

  // Fetch initial status
  useEffect(() => {
    const fetchStatus = async () => {
      try {
        const result = await testDataService.getGeneratorStatus(connectionId)
        setStatus(result)
        onStatusChange?.(result)
      } catch (error) {
        // Silent error - generator might not be initialized yet
        console.error('Failed to fetch generator status:', error)
      }
    }

    fetchStatus()
  }, [connectionId, onStatusChange])

  // Poll for status updates when running
  useEffect(() => {
    if (!status?.is_running) return

    const interval = setInterval(async () => {
      try {
        const result = await testDataService.getGeneratorStatus(connectionId)
        setStatus(result)
        onStatusChange?.(result)
      } catch (error) {
        console.error('Failed to poll generator status:', error)
      }
    }, 2000) // Poll every 2 seconds

    return () => clearInterval(interval)
  }, [status?.is_running, connectionId, onStatusChange])

  const handleStart = async () => {
    if (rate < 1 || rate > 1000) {
      addNotification({
        type: 'warning',
        title: 'Invalid Rate',
        message: 'Rate must be between 1 and 1000 messages per second',
      })
      return
    }

    try {
      setLoading(true)
      await testDataService.startAutoGenerator(connectionId, rate)
      
      // Refresh status
      const result = await testDataService.getGeneratorStatus(connectionId)
      setStatus(result)
      onStatusChange?.(result)
      
      addNotification({
        type: 'success',
        title: 'Success',
        message: `Auto-generator started at ${rate} msgs/sec`,
      })
    } catch (error) {
      const errorMessage = isAPIError(error) ? getErrorMessage(error) : 'Failed to start generator'
      addNotification({
        type: 'error',
        title: 'Error',
        message: errorMessage,
      })
    } finally {
      setLoading(false)
    }
  }

  const handleStop = async () => {
    try {
      setLoading(true)
      await testDataService.stopAutoGenerator(connectionId)
      
      // Refresh status
      const result = await testDataService.getGeneratorStatus(connectionId)
      setStatus(result)
      onStatusChange?.(result)
      
      addNotification({
        type: 'success',
        title: 'Success',
        message: 'Auto-generator stopped',
      })
    } catch (error) {
      const errorMessage = isAPIError(error) ? getErrorMessage(error) : 'Failed to stop generator'
      addNotification({
        type: 'error',
        title: 'Error',
        message: errorMessage,
      })
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="p-4 rounded-lg border border-gray-200 bg-white">
      <h3 className="text-sm font-bold text-gray-900 mb-3">Auto Message Generator</h3>

      {/* Status Display */}
      {status && (
        <div className={`mb-4 p-3 rounded border ${status.is_running ? 'bg-green-50 border-green-200' : 'bg-gray-50 border-gray-200'}`}>
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <div
                className={`w-3 h-3 rounded-full ${status.is_running ? 'bg-green-500' : 'bg-gray-500'} animate-pulse`}
              />
              <span className={`text-sm font-medium ${status.is_running ? 'text-green-900' : 'text-gray-900'}`}>
                {status.is_running ? 'Running' : 'Stopped'}
              </span>
            </div>
            {status.is_running && (
              <span className="text-xs text-gray-600">
                {status.rate_per_second} msgs/sec
              </span>
            )}
          </div>
          {status.message_count > 0 && (
            <p className="text-xs text-gray-600 mt-2">
              Total generated: {status.message_count}
            </p>
          )}
        </div>
      )}

      {/* Controls */}
      <div className="space-y-3">
        {!status?.is_running ? (
          <>
            {/* Rate Control */}
            <div>
              <label className="block text-xs font-medium text-gray-700 mb-1">
                Rate (msgs/sec): {rate}
              </label>
              <input
                type="range"
                min="1"
                max="1000"
                value={rate}
                onChange={(e) => setRate(Number(e.target.value))}
                disabled={loading}
                className="w-full"
              />
              <div className="flex justify-between text-xs text-gray-500 mt-1">
                <span>1</span>
                <span>500</span>
                <span>1000</span>
              </div>
            </div>

            {/* Start Button */}
            <button
              onClick={handleStart}
              disabled={loading}
              className="w-full px-4 py-2 bg-green-600 text-white rounded-md hover:bg-green-700 disabled:bg-gray-400 font-medium text-sm"
            >
              {loading ? 'Starting...' : 'Start Generator'}
            </button>
          </>
        ) : (
          <>
            {/* Stop Button */}
            <button
              onClick={handleStop}
              disabled={loading}
              className="w-full px-4 py-2 bg-red-600 text-white rounded-md hover:bg-red-700 disabled:bg-gray-400 font-medium text-sm"
            >
              {loading ? 'Stopping...' : 'Stop Generator'}
            </button>
          </>
        )}
      </div>

      <p className="text-xs text-gray-600 mt-3">
        {status?.is_running
          ? 'Generator is running. Press Stop to end.'
          : 'Configure settings and press Start to generate test messages.'}
      </p>
    </div>
  )
}
