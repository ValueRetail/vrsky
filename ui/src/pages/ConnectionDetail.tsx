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
        // Carry the graph model: the pipeline lives in nodes/edges, so
        // without these the clone would be an empty, undeployable pipeline.
        nodes: connection.nodes,
        edges: connection.edges,
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

  if (!connection) {
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

  const getStatusBadgeClass = (status: string) => {
    const classes: Record<string, string> = {
      running: 'badge badge-success',
      stopped: 'badge badge-warning',
      error: 'badge badge-danger',
    }
    return classes[status] || classes.stopped
  }

  return (
    <div className="min-h-screen bg-neutral-50 dark:bg-neutral-950 py-8 px-4 sm:px-6 lg:px-8">
      <div className="max-w-6xl mx-auto space-y-8">
        {/* Hero Header */}
        <div className="card-elevated animate-fade-in">
          <div className="flex flex-col sm:flex-row sm:items-start gap-6">
            {/* Info */}
            <div className="flex-1">
              <button
                onClick={() => navigate('/connections')}
                className="flex items-center gap-2 text-primary-600 dark:text-primary-400 hover:text-primary-700 dark:hover:text-primary-300 font-medium mb-4 transition-colors duration-base"
              >
                <span>←</span>
                <span>Back to Connections</span>
              </button>
              <h1 className="text-4xl sm:text-5xl font-bold text-neutral-900 dark:text-neutral-50 mb-2">
                {connection.name}
              </h1>
              {connection.description && (
                <p className="text-lg text-neutral-600 dark:text-neutral-400">
                  {connection.description}
                </p>
              )}
            </div>

            {/* Status Badge */}
            <div className="flex-shrink-0">
              <span className={getStatusBadgeClass(connection.status)}>
                {connection.status.charAt(0).toUpperCase() + connection.status.slice(1)}
              </span>
            </div>
          </div>
        </div>

        {/* Action Buttons */}
        <div className="flex flex-wrap gap-2 animate-fade-in">
          {connection.status === 'running' ? (
            <button
              onClick={handleStop}
              disabled={actionLoading}
              className="btn-danger"
            >
              {actionLoading ? 'Stopping...' : 'Stop Connection'}
            </button>
          ) : (
            <button
              onClick={handleStart}
              disabled={actionLoading}
              className="btn-success"
            >
              {actionLoading ? 'Starting...' : 'Start Connection'}
            </button>
          )}
          <button
            onClick={() => navigate(`/connections/${connection.id}/edit`)}
            className="btn-primary"
          >
            Edit
          </button>
          <button
            onClick={handleClone}
            disabled={actionLoading}
            className="btn-secondary"
          >
            Clone
          </button>
          <button
            onClick={handleDelete}
            disabled={actionLoading}
            className="btn-outline"
          >
            Delete
          </button>
        </div>

        {/* Metrics Section - Show only when running */}
        {connection.status === 'running' && (
          <div className="space-y-6 animate-fade-in">
            <div>
              <h2 className="text-3xl font-bold text-neutral-900 dark:text-neutral-50">Real-time Metrics</h2>
              <p className="text-neutral-600 dark:text-neutral-400 mt-2">Live pipeline performance monitoring</p>
            </div>

            {/* Key Metrics Cards */}
            <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
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

          {/* Pipeline Flow Visualization — driven by the connection's real graph */}
          <PipelineFlowVisualization
            metrics={metrics}
            nodes={connection.nodes}
            edges={connection.edges}
            status={connection.status}
          />

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
              <p className="text-xs text-neutral-500 dark:text-neutral-600 text-right">
                Last updated: {new Date(metrics.last_updated).toLocaleTimeString()}
              </p>
            )}
          </div>
        )}

        {/* Test Data Section - Show when running */}
        {connection.status === 'running' && (
          <div className="space-y-6 animate-fade-in">
            <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
              <div>
                <h2 className="text-3xl font-bold text-neutral-900 dark:text-neutral-50">Test Messages</h2>
                <p className="text-neutral-600 dark:text-neutral-400 mt-2">Send test data through your pipeline</p>
              </div>
              <button
                onClick={() => navigate(`/connections/${connection.id}/test-data`)}
                className="btn-primary"
              >
                Full Test Interface
              </button>
            </div>

            <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
              <TestMessageForm connectionId={connection.id} />
            </div>

            <p className="text-sm text-neutral-600 dark:text-neutral-400">
              Send test messages to verify your pipeline configuration. View detailed test results in the{' '}
              <button
                onClick={() => navigate(`/connections/${connection.id}/test-data`)}
                className="text-primary-600 dark:text-primary-400 hover:text-primary-700 dark:hover:text-primary-300 font-medium"
              >
                full test interface
              </button>
              .
            </p>
          </div>
        )}

      </div>
    </div>
  )
}
