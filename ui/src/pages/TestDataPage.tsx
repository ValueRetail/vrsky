/**
 * TestData Page
 * Comprehensive test data management interface
 */

import { useState, useEffect } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { connectionService } from '@/services/connectionService'
import { useMessageLogStore } from '@/store/messageLogStore'
import { TestMessageForm } from '@/components/TestData/TestMessageForm'
import { AutoGeneratorControls } from '@/components/TestData/AutoGeneratorControls'
import { MessageLog } from '@/components/TestData/MessageLog'
import { useUIStore } from '@/store/uiStore'
import { isAPIError, getErrorMessage } from '@/utils/errors'
import type { Connection } from '@/types/models'

export default function TestDataPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [connection, setConnection] = useState<Connection | null>(null)
  const [loading, setLoading] = useState(true)
  const { addNotification } = useUIStore()
  const { getMessages, addMessage } = useMessageLogStore()

  useEffect(() => {
    if (!id) {
      navigate('/404')
      return
    }

    const loadConnection = async () => {
      try {
        setLoading(true)
        const data = await connectionService.get(id)
        setConnection(data)
      } catch (error) {
        const message = isAPIError(error) ? getErrorMessage(error) : 'Failed to load connection'
        addNotification({
          type: 'error',
          title: 'Error',
          message,
        })
        navigate('/')
      } finally {
        setLoading(false)
      }
    }

    loadConnection()
  }, [id, navigate, addNotification])

  if (loading) {
    return (
      <div className="flex items-center justify-center min-h-screen">
        <div className="animate-spin rounded-full h-12 w-12 border-b-2 border-blue-600"></div>
      </div>
    )
  }

  if (!connection || !id) {
    return (
      <div className="max-w-4xl mx-auto px-4 py-8">
        <div className="text-center">
          <h1 className="text-2xl font-bold text-gray-900">Connection not found</h1>
          <button
            onClick={() => navigate('/')}
            className="mt-4 px-4 py-2 bg-blue-600 text-white rounded-md hover:bg-blue-700"
          >
            Back to Dashboard
          </button>
        </div>
      </div>
    )
  }

  const messages = getMessages(id)

  const handleMessageSent = () => {
    // Add a new test message entry
    addMessage(id, {
      id: `msg-${Date.now()}`,
      timestamp: new Date().toISOString(),
      status: 'sent',
      message: '{"test": "message"}',
    })
  }

  return (
    <div className="max-w-4xl mx-auto px-4 py-8">
      {/* Header */}
      <div className="mb-8">
        <button onClick={() => navigate(`/connections/${id}`)} className="text-blue-600 hover:text-blue-700 mb-2">
          ← Back to Connection
        </button>
        <h1 className="text-3xl font-bold text-gray-900">Test Data & Messages</h1>
        <p className="text-gray-600 mt-1">{connection.name}</p>
      </div>

      {/* Status Warning */}
      {connection.status !== 'running' && (
        <div className="mb-6 p-4 bg-yellow-50 border border-yellow-200 rounded-lg">
          <p className="text-sm text-yellow-900">
            <strong>Note:</strong> This connection is {connection.status}. Start it to test message sending.
          </p>
        </div>
      )}

      {/* Content Grid */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {/* Left Column - Controls */}
        <div className="space-y-6">
          {connection.status === 'running' && (
            <>
              <TestMessageForm connectionId={id} onMessageSent={handleMessageSent} />
              <AutoGeneratorControls connectionId={id} />
            </>
          )}

          {connection.status !== 'running' && (
            <div className="p-4 rounded-lg border border-gray-200 bg-gray-50 text-center">
              <p className="text-gray-600 text-sm">Start the connection to send test messages</p>
              <button
                onClick={() => navigate(`/connections/${id}`)}
                className="mt-3 px-4 py-2 bg-blue-600 text-white rounded-md hover:bg-blue-700 text-sm"
              >
                Go to Connection
              </button>
            </div>
          )}
        </div>

        {/* Right Column - Message Log */}
        <div>
          <MessageLog messages={messages} pageSize={15} />
        </div>
      </div>

      {/* Info Section */}
      <div className="mt-8 p-4 bg-blue-50 border border-blue-200 rounded-lg">
        <h3 className="text-sm font-bold text-blue-900 mb-2">How to test your pipeline:</h3>
        <ul className="text-sm text-blue-900 space-y-1 list-disc list-inside">
          <li>Send individual test messages using the form above</li>
          <li>Or use the auto-generator to send messages at a specific rate</li>
          <li>Messages are processed through your pipeline in real-time</li>
          <li>Check the connection metrics to see throughput and errors</li>
        </ul>
      </div>
    </div>
  )
}
