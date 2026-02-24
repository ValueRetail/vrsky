/**
 * TestMessageForm Component
 * Form to send a single test message to a connection
 */

import { useState } from 'react'
import { testDataService } from '@/services/testDataService'
import { useUIStore } from '@/store/uiStore'
import { isAPIError, getErrorMessage } from '@/utils/errors'

interface TestMessageFormProps {
  connectionId: string
  onMessageSent?: () => void
}

export function TestMessageForm({ connectionId, onMessageSent }: TestMessageFormProps) {
  const [message, setMessage] = useState('{"test": "message"}')
  const [loading, setLoading] = useState(false)
  const { addNotification } = useUIStore()

  const handleSendMessage = async () => {
    if (!message.trim()) {
      addNotification({
        type: 'warning',
        title: 'Warning',
        message: 'Please enter a message',
      })
      return
    }

    try {
      setLoading(true)
      await testDataService.sendTestMessage(connectionId, message)
      addNotification({
        type: 'success',
        title: 'Success',
        message: 'Test message sent',
      })
      setMessage('{"test": "message"}')
      onMessageSent?.()
    } catch (error) {
      const errorMessage = isAPIError(error) ? getErrorMessage(error) : 'Failed to send test message'
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
      <h3 className="text-sm font-bold text-gray-900 mb-3">Send Test Message</h3>
      
      <div className="space-y-3">
        <textarea
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          placeholder='Enter JSON message, e.g., {"test": "data"}'
          className="w-full p-3 border border-gray-300 rounded-md text-sm font-mono focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
          rows={4}
          disabled={loading}
        />
        
        <div className="flex gap-2">
          <button
            onClick={handleSendMessage}
            disabled={loading || !message.trim()}
            className="flex-1 px-4 py-2 bg-blue-600 text-white rounded-md hover:bg-blue-700 disabled:bg-gray-400 font-medium text-sm"
          >
            {loading ? 'Sending...' : 'Send Message'}
          </button>
          <button
            onClick={() => setMessage('{"test": "message"}')}
            disabled={loading}
            className="px-4 py-2 bg-gray-200 text-gray-900 rounded-md hover:bg-gray-300 disabled:bg-gray-400 font-medium text-sm"
          >
            Reset
          </button>
        </div>
      </div>

      <p className="text-xs text-gray-600 mt-3">
        Tip: Enter a valid JSON message to send through the pipeline for testing
      </p>
    </div>
  )
}
