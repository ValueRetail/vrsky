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
      <div className="flex items-center justify-center min-h-screen bg-neutral-50 dark:bg-neutral-950">
        <div className="space-y-4 text-center">
          <div className="flex justify-center">
            <div className="animate-spin rounded-full h-16 w-16 border-4 border-primary-200 dark:border-primary-900 border-t-primary-600 dark:border-t-primary-400"></div>
          </div>
          <p className="text-neutral-600 dark:text-neutral-400 font-medium">Loading connection...</p>
        </div>
      </div>
    )
  }

  if (!connection || !id) {
    return (
      <div className="min-h-screen bg-neutral-50 dark:bg-neutral-950 flex items-center justify-center px-4">
        <div className="card-elevated text-center py-12 max-w-md">
          <div className="text-5xl mb-4">⚠️</div>
          <h1 className="text-2xl font-bold text-neutral-900 dark:text-neutral-50 mb-2">Connection not found</h1>
          <p className="text-neutral-600 dark:text-neutral-400 mb-6">The connection you're looking for doesn't exist or has been deleted.</p>
          <button
            onClick={() => navigate('/')}
            className="btn-primary"
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
    <div className="min-h-screen bg-neutral-50 dark:bg-neutral-950 py-8 px-4 sm:px-6 lg:px-8">
      <div className="max-w-6xl mx-auto space-y-6">
        {/* Header */}
        <div className="animate-fade-in">
          <button
            onClick={() => navigate(`/connections/${id}`)}
            className="flex items-center gap-2 text-primary-600 dark:text-primary-400 hover:text-primary-700 dark:hover:text-primary-300 font-medium mb-4 transition-colors duration-base"
          >
            <span>←</span>
            <span>Back to Connection</span>
          </button>
          <div className="space-y-2">
            <h1 className="text-4xl sm:text-5xl font-bold bg-gradient-to-r from-primary-600 to-secondary-600 dark:from-primary-400 dark:to-secondary-400 bg-clip-text text-transparent">
              Test Data & Messages
            </h1>
            <p className="text-lg text-neutral-600 dark:text-neutral-400">
              Test {connection.name}
            </p>
          </div>
        </div>

        {/* Status Warning */}
        {connection.status !== 'running' && (
          <div className="alert alert-warning animate-fade-in">
            <div className="flex gap-3">
              <span className="text-xl">⚠️</span>
              <div>
                <p className="font-semibold">Connection is {connection.status}</p>
                <p className="text-sm">Start the connection to send test messages.</p>
              </div>
            </div>
          </div>
        )}

        {/* Success Message */}
        {connection.status === 'running' && (
          <div className="alert alert-success animate-fade-in">
            <div className="flex gap-3">
              <span className="text-xl">✓</span>
              <div>
                <p className="font-semibold">Connection is running</p>
                <p className="text-sm">Send test messages to verify your pipeline.</p>
              </div>
            </div>
          </div>
        )}

        {/* Content Grid */}
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 animate-fade-in">
          {/* Left Column - Controls */}
          <div className="space-y-6">
            {connection.status === 'running' ? (
              <>
                <div className="card-elevated">
                  <TestMessageForm connectionId={id} onMessageSent={handleMessageSent} />
                </div>
                <div className="card-elevated">
                  <AutoGeneratorControls connectionId={id} />
                </div>
              </>
            ) : (
              <div className="card-elevated text-center py-12 space-y-4">
                <div className="text-4xl">🔒</div>
                <div>
                  <p className="text-neutral-600 dark:text-neutral-400 font-medium mb-4">
                    Start the connection to test messages
                  </p>
                  <button
                    onClick={() => navigate(`/connections/${id}`)}
                    className="btn-primary"
                  >
                    Go to Connection
                  </button>
                </div>
              </div>
            )}
          </div>

          {/* Right Column - Message Log */}
          <div className="card-elevated">
            <h3 className="text-lg font-bold text-neutral-900 dark:text-neutral-50 mb-4">Message Log</h3>
            <MessageLog messages={messages} pageSize={15} />
          </div>
        </div>

        {/* Info Section */}
        <div className="card-accent animate-fade-in">
          <h3 className="text-lg font-bold text-neutral-900 dark:text-neutral-50 mb-4">How to test your pipeline</h3>
          <ul className="space-y-2 text-neutral-600 dark:text-neutral-400">
            <li className="flex gap-3">
              <span className="font-semibold text-primary-600 dark:text-primary-400">1.</span>
              <span>Send individual test messages using the form above</span>
            </li>
            <li className="flex gap-3">
              <span className="font-semibold text-primary-600 dark:text-primary-400">2.</span>
              <span>Or use the auto-generator to send messages at a specific rate</span>
            </li>
            <li className="flex gap-3">
              <span className="font-semibold text-primary-600 dark:text-primary-400">3.</span>
              <span>Messages are processed through your pipeline in real-time</span>
            </li>
            <li className="flex gap-3">
              <span className="font-semibold text-primary-600 dark:text-primary-400">4.</span>
              <span>Check the connection metrics to see throughput and errors</span>
            </li>
          </ul>
        </div>
      </div>
    </div>
  )
}
