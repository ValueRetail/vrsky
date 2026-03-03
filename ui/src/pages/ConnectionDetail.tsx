import { useEffect, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useUIStore } from '../store/uiStore'
import { connectionService } from '../services/connectionService'
import type { Connection } from '../types/models'
import { isAPIError, getErrorMessage } from '../utils/errors'
import { useMetrics } from '../hooks/useMetrics'
import { MetricsCard } from '../components/MetricsDisplay/MetricsCard'
import { MetricsChart } from '../components/MetricsDisplay/MetricsChart'
import { PipelineFlowVisualization } from '../components/MetricsDisplay/PipelineFlowVisualization'
import { MessageProgressBar } from '../components/MetricsDisplay/MessageProgressBar'
import { TestMessageForm } from '../components/TestData/TestMessageForm'

export default function ConnectionDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [connection, setConnection] = useState<Connection | null>(null)
  const [loading, setLoading] = useState(true)
  const [actionLoading, setActionLoading] = useState(false)
  const { addNotification, showConfirmDialog, hideConfirmDialog } = useUIStore()
  
  // Use metrics hook for real-time SSE updates (only if connection is running)
  const metrics = useMetrics(id || '', {
    enabled: !!id && connection?.status === 'running',
    onError: (error) => {
      console.error('Metrics stream error:', error)
      // Silently handle SSE errors - connection might not be available yet
    },
  })

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

  const handleStart = async () => {
    if (!connection) return

    try {
      setActionLoading(true)
      const updated = await connectionService.start(connection.id)
      setConnection(updated)
      addNotification({
        type: 'success',
        title: 'Success',
        message: 'Connection started',
      })
    } catch (error) {
      const message = isAPIError(error) ? getErrorMessage(error) : 'Failed to start connection'
      addNotification({
        type: 'error',
        title: 'Error',
        message,
      })
    } finally {
      setActionLoading(false)
    }
  }

  const handleStop = async () => {
    if (!connection) return

    try {
      setActionLoading(true)
      const updated = await connectionService.stop(connection.id)
      setConnection(updated)
      addNotification({
        type: 'success',
        title: 'Success',
        message: 'Connection stopped',
      })
    } catch (error) {
      const message = isAPIError(error) ? getErrorMessage(error) : 'Failed to stop connection'
      addNotification({
        type: 'error',
        title: 'Error',
        message,
      })
    } finally {
      setActionLoading(false)
    }
  }

  const handleClone = async () => {
    if (!connection) return
    try {
      setActionLoading(true)
      const clonePayload = {
        name: `Copy of ${connection.name}`,
        description: connection.description,
        source_config: connection.source_config,
        converter_config: connection.converter_config,
        filter_config: connection.filter_config,
        destination_config: connection.destination_config,
      }
      const newConnection = await connectionService.create(clonePayload as unknown)
      addNotification({ type: 'success', title: 'Cloned', message: `"${newConnection.name}" created` })
      navigate(`/connections/${newConnection.id}`)
    } catch (error) {
      const message = isAPIError(error) ? getErrorMessage(error) : 'Failed to clone connection'
      addNotification({ type: 'error', title: 'Error', message })
    } finally {
      setActionLoading(false)
    }
  }

  const handleDelete = () => {
    showConfirmDialog({
      title: 'Delete Connection',
      message: `Are you sure you want to delete "${connection?.name}"? This action cannot be undone.`,
      confirmLabel: 'Delete',
      cancelLabel: 'Cancel',
      destructive: true,
      onConfirm: handleConfirmDelete,
    })
  }

  const handleConfirmDelete = async () => {
    if (!connection) return

    try {
      hideConfirmDialog()
      setActionLoading(true)
      await connectionService.delete(connection.id)
      addNotification({
        type: 'success',
        title: 'Success',
        message: 'Connection deleted',
      })
      navigate('/')
    } catch (error) {
      const message = isAPIError(error) ? getErrorMessage(error) : 'Failed to delete connection'
      addNotification({
        type: 'error',
        title: 'Error',
        message,
      })
    } finally {
      setActionLoading(false)
    }
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center min-h-screen">
        <div className="animate-spin rounded-full h-12 w-12 border-b-2 border-blue-600"></div>
      </div>
    )
  }

  if (!connection) {
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

  const statusColors: Record<string, string> = {
    running: 'bg-green-100 text-green-800',
    stopped: 'bg-gray-100 text-gray-800',
    error: 'bg-red-100 text-red-800',
  }

  return (
    <div className="max-w-4xl mx-auto px-4 py-8">
      {/* Header */}
      <div className="flex items-center justify-between mb-8">
        <div>
          <button
            onClick={() => navigate('/')}
            className="text-blue-600 hover:text-blue-700 mb-2"
          >
            ← Back
          </button>
          <h1 className="text-3xl font-bold text-gray-900">{connection.name}</h1>
          <p className="text-gray-600 mt-1">{connection.description}</p>
        </div>
        <div className={`px-4 py-2 rounded-full font-medium ${statusColors[connection.status] || statusColors.stopped}`}>
          {connection.status.charAt(0).toUpperCase() + connection.status.slice(1)}
        </div>
      </div>

      {/* Action Buttons */}
      <div className="flex gap-3 mb-8">
        {connection.status === 'running' ? (
          <button
            onClick={handleStop}
            disabled={actionLoading}
            className="px-4 py-2 bg-red-600 text-white rounded-md hover:bg-red-700 disabled:bg-gray-400"
          >
            {actionLoading ? 'Stopping...' : 'Stop'}
          </button>
        ) : (
          <button
            onClick={handleStart}
            disabled={actionLoading}
            className="px-4 py-2 bg-green-600 text-white rounded-md hover:bg-green-700 disabled:bg-gray-400"
          >
            {actionLoading ? 'Starting...' : 'Start'}
          </button>
        )}
        <button
          onClick={() => navigate(`/connections/${connection.id}/edit`)}
          className="px-4 py-2 bg-blue-600 text-white rounded-md hover:bg-blue-700"
        >
          Edit
        </button>
        <button
          onClick={handleClone}
          disabled={actionLoading}
          className="px-4 py-2 bg-purple-600 text-white rounded-md hover:bg-purple-700 disabled:bg-gray-400"
        >
          Clone
        </button>
        <button
          onClick={handleDelete}
          disabled={actionLoading}
          className="px-4 py-2 bg-red-600 text-white rounded-md hover:bg-red-700 disabled:bg-gray-400"
        >
          Delete
        </button>
      </div>

      {/* Metrics Section - Show only when running */}
      {connection.status === 'running' && (
        <div className="space-y-6">
          <h2 className="text-2xl font-bold text-gray-900">Real-time Metrics</h2>

          {/* Key Metrics Cards */}
          <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
            <MetricsCard
              label="Throughput"
              value={metrics?.throughput_mps.toFixed(2) || '0'}
              unit="msgs/sec"
              color="blue"
              loading={!metrics}
              trend={metrics && metrics.throughput_mps > 0 ? 'up' : 'stable'}
            />
            <MetricsCard
              label="Messages In"
              value={metrics?.total_messages_in || 0}
              color="green"
              loading={!metrics}
            />
            <MetricsCard
              label="Messages Out"
              value={metrics?.total_messages_out || 0}
              color="purple"
              loading={!metrics}
            />
            <MetricsCard
              label="Errors"
              value={metrics?.total_errors || 0}
              color={metrics && metrics.total_errors > 0 ? 'red' : 'yellow'}
              loading={!metrics}
              trend={metrics && metrics.errors_per_second > 0 ? 'up' : 'stable'}
              trendValue={metrics?.errors_per_second.toFixed(2)}
            />
          </div>

          {/* Pipeline Flow Visualization */}
          <PipelineFlowVisualization metrics={metrics} />

          {/* Message Progress Bar */}
          {metrics && <MessageProgressBar
            messagesIn={metrics.total_messages_in}
            messagesOut={metrics.total_messages_out}
            throughputMps={metrics.throughput_mps}
          />}

          {/* Component Metrics Chart */}
          {metrics && (
            <MetricsChart
              title="Component Processing"
              data={[
                {
                  label: 'Consumer',
                  value: metrics.components.consumer.messages_processed,
                  color: '#3b82f6',
                },
                {
                  label: 'Converter',
                  value: metrics.components.converter.messages_processed,
                  color: '#10b981',
                },
                {
                  label: 'Filter',
                  value: metrics.components.filter.messages_processed,
                  color: '#f59e0b',
                },
                {
                  label: 'Producer',
                  value: metrics.components.producer.messages_sent,
                  color: '#8b5cf6',
                },
              ]}
              maxValue={Math.max(
                metrics.components.consumer.messages_processed,
                metrics.components.converter.messages_processed,
                metrics.components.filter.messages_processed,
                metrics.components.producer.messages_sent,
                1
              )}
            />
          )}

          {/* Error Distribution */}
          {metrics && (
            <MetricsChart
              title="Errors by Component"
              data={[
                {
                  label: 'Consumer',
                  value: metrics.components.consumer.errors,
                  color: '#ef4444',
                },
                {
                  label: 'Converter',
                  value: metrics.components.converter.errors,
                  color: '#ef4444',
                },
                {
                  label: 'Filter',
                  value: metrics.components.filter.errors,
                  color: '#ef4444',
                },
                {
                  label: 'Producer',
                  value: metrics.components.producer.errors,
                  color: '#ef4444',
                },
              ]}
              maxValue={Math.max(
                metrics.components.consumer.errors,
                metrics.components.converter.errors,
                metrics.components.filter.errors,
                metrics.components.producer.errors,
                1
              )}
            />
          )}

          {/* Last Update */}
          {metrics && (
            <p className="text-xs text-gray-500 text-right">
              Last updated: {new Date(metrics.last_updated).toLocaleTimeString()}
            </p>
          )}
        </div>
      )}

      {/* Test Data Section - Show when running */}
      {connection.status === 'running' && (
        <div className="space-y-6">
          <div className="flex items-center justify-between">
            <h2 className="text-2xl font-bold text-gray-900">Test Messages</h2>
            <button
              onClick={() => navigate(`/connections/${connection.id}/test-data`)}
              className="px-4 py-2 bg-blue-600 text-white rounded-md hover:bg-blue-700 text-sm font-medium"
            >
              Full Test Interface
            </button>
          </div>

          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
            <TestMessageForm connectionId={connection.id} />
          </div>

          <p className="text-sm text-gray-600">
            Send test messages to verify your pipeline configuration. View detailed test results in the{' '}
            <button
              onClick={() => navigate(`/connections/${connection.id}/test-data`)}
              className="text-blue-600 hover:text-blue-700 font-medium"
            >
              full test interface
            </button>
            .
          </p>
        </div>
      )}

      {/* Configuration Details */}
      <div className="space-y-8">
        {/* Source Configuration */}
        <section className="bg-white rounded-lg border border-gray-200 p-6">
          <h2 className="text-xl font-bold text-gray-900 mb-4">Source Configuration</h2>
          <div className="bg-gray-50 rounded p-4 overflow-x-auto">
            <pre className="text-sm font-mono text-gray-800">
              {JSON.stringify(connection.source_config, null, 2)}
            </pre>
          </div>
        </section>

        {/* Converter Configuration */}
        <section className="bg-white rounded-lg border border-gray-200 p-6">
          <h2 className="text-xl font-bold text-gray-900 mb-4">Converter Configuration</h2>
          <div className="bg-gray-50 rounded p-4 overflow-x-auto">
            <pre className="text-sm font-mono text-gray-800">
              {JSON.stringify(connection.converter_config, null, 2)}
            </pre>
          </div>
        </section>

        {/* Filter Configuration */}
        <section className="bg-white rounded-lg border border-gray-200 p-6">
          <h2 className="text-xl font-bold text-gray-900 mb-4">Filter Configuration</h2>
          <div className="bg-gray-50 rounded p-4 overflow-x-auto">
            <pre className="text-sm font-mono text-gray-800">
              {JSON.stringify(connection.filter_config, null, 2)}
            </pre>
          </div>
        </section>

        {/* Destination Configuration */}
        <section className="bg-white rounded-lg border border-gray-200 p-6">
          <h2 className="text-xl font-bold text-gray-900 mb-4">Destination Configuration</h2>
          <div className="bg-gray-50 rounded p-4 overflow-x-auto">
            <pre className="text-sm font-mono text-gray-800">
              {JSON.stringify(connection.destination_config, null, 2)}
            </pre>
          </div>
        </section>

        {/* Metadata */}
        <section className="bg-white rounded-lg border border-gray-200 p-6">
          <h2 className="text-xl font-bold text-gray-900 mb-4">Details</h2>
          <div className="grid grid-cols-2 gap-4">
            <div>
              <p className="text-sm text-gray-600">ID</p>
              <p className="text-gray-900 font-mono text-sm">{connection.id}</p>
            </div>
            <div>
              <p className="text-sm text-gray-600">Status</p>
              <p className="text-gray-900">{connection.status}</p>
            </div>
            <div>
              <p className="text-sm text-gray-600">Created</p>
              <p className="text-gray-900">{new Date(connection.created_at).toLocaleString()}</p>
            </div>
            <div>
              <p className="text-sm text-gray-600">Last Updated</p>
              <p className="text-gray-900">{new Date(connection.updated_at).toLocaleString()}</p>
            </div>
          </div>
        </section>
      </div>
    </div>
  )
}
