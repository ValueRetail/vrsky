import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { connectionService } from '../services/connectionService'
import { useUIStore } from '../store/uiStore'
import { isAPIError, getErrorMessage } from '../utils/errors'
import type { Connection, ConnectionStatus } from '../types/models'

type StatusFilter = 'all' | ConnectionStatus

const PAGE_SIZE = 10

// Legacy source_config/destination_config are empty on graph-based
// (builder-created) connections, where the pipeline lives in nodes/edges.
// Prefer the legacy field, then fall back to the matching node, then a dash —
// reading .type off an absent source_config would throw and crash the list.
const nodeLabel = (connection: Connection, role: string): string => {
  const node = connection.nodes?.find(n => n.type === role)
  const cfgType = node?.config?.type
  return typeof cfgType === 'string' ? cfgType : (node?.type ?? '—')
}
const sourceLabel = (c: Connection): string => c.source_config?.type || nodeLabel(c, 'consumer')
const destinationLabel = (c: Connection): string => c.destination_config?.type || nodeLabel(c, 'producer')
const formatDate = (value?: string): string => {
  if (!value) return '—'
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleDateString()
}

export default function ConnectionsList() {
  const navigate = useNavigate()
  const { addNotification, showConfirmDialog, hideConfirmDialog } = useUIStore()

  const [connections, setConnections] = useState<Connection[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [actionLoading, setActionLoading] = useState(false)
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')

  const totalPages = Math.ceil(total / PAGE_SIZE)

  useEffect(() => {
    const load = async () => {
      try {
        setLoading(true)
        const response = await connectionService.list(page, PAGE_SIZE) as any
        const items = response.connections || response.data || []
        setConnections(items as Connection[])
        setTotal(response.total || 0)
      } catch (error) {
        const message = isAPIError(error) ? getErrorMessage(error) : 'Failed to load connections'
        addNotification({ type: 'error', title: 'Error', message })
      } finally {
        setLoading(false)
      }
    }
    load()
  }, [page, addNotification])

  const filtered = connections && connections.length > 0
    ? (statusFilter === 'all'
      ? connections
      : connections.filter(c => c.status === statusFilter))
    : []

  const handleDelete = (connection: Connection) => {
    showConfirmDialog({
      title: 'Delete Connection',
      message: `Are you sure you want to delete "${connection.name}"? This action cannot be undone.`,
      confirmLabel: 'Delete',
      cancelLabel: 'Cancel',
      destructive: true,
      onConfirm: async () => {
        try {
          hideConfirmDialog()
          setActionLoading(true)
          await connectionService.delete(connection.id)
          addNotification({ type: 'success', title: 'Deleted', message: `"${connection.name}" deleted` })
          setConnections(prev => prev.filter(c => c.id !== connection.id))
          setTotal(prev => prev - 1)
        } catch (error) {
          const message = isAPIError(error) ? getErrorMessage(error) : 'Failed to delete connection'
          addNotification({ type: 'error', title: 'Error', message })
        } finally {
          setActionLoading(false)
        }
      },
    })
  }

  const handleStop = async (connection: Connection) => {
    try {
      setActionLoading(true)
      const updated = await connectionService.stop(connection.id)
      addNotification({ type: 'success', title: 'Stopped', message: `"${connection.name}" stopped` })
      // Reflect the server's actual status (not an optimistic guess) so the
      // list stays consistent with the detail page.
      setConnections(prev => prev.map(c => c.id === connection.id ? { ...c, status: (updated?.status ?? 'stopped') as ConnectionStatus } : c))
    } catch (error) {
      const message = isAPIError(error) ? getErrorMessage(error) : 'Failed to stop connection'
      addNotification({ type: 'error', title: 'Error', message })
    } finally {
      setActionLoading(false)
    }
  }

  const handleStart = async (connection: Connection) => {
    try {
      setActionLoading(true)
      const updated = await connectionService.start(connection.id)
      addNotification({ type: 'success', title: 'Started', message: `"${connection.name}" started` })
      // Reflect the server's actual status (not an optimistic guess) so the
      // list stays consistent with the detail page.
      setConnections(prev => prev.map(c => c.id === connection.id ? { ...c, status: (updated?.status ?? 'running') as ConnectionStatus } : c))
    } catch (error) {
      const message = isAPIError(error) ? getErrorMessage(error) : 'Failed to start connection'
      addNotification({ type: 'error', title: 'Error', message })
    } finally {
      setActionLoading(false)
    }
  }

  const statusBadge = (status: ConnectionStatus) => {
    const classes: Record<ConnectionStatus, string> = {
      running: 'badge badge-success',
      stopped: 'badge badge-warning',
      error: 'badge badge-danger',
    }
    return (
      <span className={classes[status]}>
        {status.charAt(0).toUpperCase() + status.slice(1)}
      </span>
    )
  }

  return (
    <div className="min-h-screen bg-neutral-50 dark:bg-neutral-950 py-8 px-4 sm:px-6 lg:px-8" data-testid="connections-list">
      <div className="max-w-7xl mx-auto space-y-6">
        {/* Header Section */}
        <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4 animate-fade-in">
          <div>
            <h1 className="text-4xl sm:text-5xl font-bold bg-gradient-to-r from-primary-600 to-secondary-600 dark:from-primary-400 dark:to-secondary-400 bg-clip-text text-transparent">
              Connections
            </h1>
            <p className="text-neutral-600 dark:text-neutral-400 mt-2">
              Manage your data pipeline integrations
            </p>
          </div>
          <Link
            to="/connections/create"
            className="btn-primary btn-lg"
          >
            New Connection
          </Link>
        </div>

        {/* Filters Section */}
        <div className="flex flex-col sm:flex-row gap-4 items-start sm:items-center animate-fade-in">
          <label className="label">Status Filter:</label>
          <select
            value={statusFilter}
            onChange={e => setStatusFilter(e.target.value as StatusFilter)}
            className="input-base"
          >
            <option value="all">All Connections</option>
            <option value="running">Running</option>
            <option value="stopped">Stopped</option>
            <option value="error">Error</option>
          </select>
        </div>

        {/* Content Section */}
        {loading ? (
          <div className="space-y-4">
            {Array.from({ length: 5 }).map((_, i) => (
              <div key={i} className="h-20 bg-neutral-200 dark:bg-neutral-800 rounded-lg animate-pulse" />
            ))}
          </div>
        ) : filtered.length === 0 ? (
          <div className="card-elevated text-center py-16 animate-fade-in">
            <div className="space-y-4">
              <div className="text-5xl">∅</div>
              <p className="text-lg text-neutral-600 dark:text-neutral-400">
                No connections yet
              </p>
              <p className="text-neutral-500 dark:text-neutral-500 mb-6">
                Create your first connection to start building integrations
              </p>
              <Link
                to="/connections/create"
                className="btn-primary inline-block"
              >
                Create Connection
              </Link>
            </div>
          </div>
        ) : (
          <div className="space-y-4 animate-fade-in">
            {filtered.map(connection => (
              <div
                key={connection.id}
                className="card-elevated hover:shadow-lg transition-shadow duration-base group"
              >
                <div className="flex flex-col sm:flex-row sm:items-center gap-4">
                  {/* Connection Info */}
                  <div className="flex-1 min-w-0">
                    <h3 className="text-lg font-semibold text-neutral-900 dark:text-neutral-50 truncate">
                      {connection.name}
                    </h3>
                    {connection.description && (
                      <p className="text-sm text-neutral-600 dark:text-neutral-400 truncate">
                        {connection.description}
                      </p>
                    )}
                    <div className="flex flex-wrap gap-4 mt-3 text-sm text-neutral-600 dark:text-neutral-400">
                      <span>Source: <span className="font-medium text-neutral-900 dark:text-neutral-50">{sourceLabel(connection)}</span></span>
                      <span>Destination: <span className="font-medium text-neutral-900 dark:text-neutral-50">{destinationLabel(connection)}</span></span>
                      <span>Created: <span className="font-medium text-neutral-900 dark:text-neutral-50">{formatDate(connection.created_at)}</span></span>
                    </div>
                  </div>

                  {/* Status & Actions */}
                  <div className="flex flex-col sm:flex-row items-start sm:items-center gap-3 sm:gap-4">
                    <div>
                      {statusBadge(connection.status)}
                    </div>
                    <div className="flex gap-2 w-full sm:w-auto">
                      {connection.status === 'running' ? (
                        <button
                          onClick={() => handleStop(connection)}
                          disabled={actionLoading}
                          className="btn-secondary btn-sm flex-1 sm:flex-none text-orange-600 border-orange-300 hover:bg-orange-50 dark:text-orange-400 dark:border-orange-700 dark:hover:bg-orange-900/20"
                        >
                          Stop
                        </button>
                      ) : (
                        <button
                          onClick={() => handleStart(connection)}
                          disabled={actionLoading}
                          className="btn-secondary btn-sm flex-1 sm:flex-none text-green-600 border-green-300 hover:bg-green-50 dark:text-green-400 dark:border-green-700 dark:hover:bg-green-900/20"
                        >
                          Start
                        </button>
                      )}
                      <button
                        onClick={() => navigate(`/connections/${connection.id}`)}
                        className="btn-secondary btn-sm flex-1 sm:flex-none"
                      >
                        View
                      </button>
                      <button
                        onClick={() => navigate(`/connections/${connection.id}/edit`)}
                        className="btn-secondary btn-sm flex-1 sm:flex-none"
                      >
                        Edit
                      </button>
                      <button
                        onClick={() => handleDelete(connection)}
                        disabled={actionLoading}
                        className="btn-danger btn-sm flex-1 sm:flex-none"
                      >
                        Delete
                      </button>
                    </div>
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}

        {/* Pagination */}
        {totalPages > 1 && (
          <div className="flex items-center justify-between mt-8 animate-fade-in">
            <button
              onClick={() => setPage(p => Math.max(1, p - 1))}
              disabled={page === 1}
              className="btn-secondary"
            >
              Previous
            </button>
            <span className="text-sm font-medium text-neutral-600 dark:text-neutral-400">
              Page {page} of {totalPages}
            </span>
            <button
              onClick={() => setPage(p => Math.min(totalPages, p + 1))}
              disabled={page === totalPages}
              className="btn-secondary"
            >
              Next
            </button>
          </div>
        )}
      </div>
    </div>
  )
}
