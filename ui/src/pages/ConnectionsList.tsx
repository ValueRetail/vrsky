import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { connectionService } from '../services/connectionService'
import { useUIStore } from '../store/uiStore'
import { isAPIError, getErrorMessage } from '../utils/errors'
import type { Connection, ConnectionStatus } from '../types/models'

type StatusFilter = 'all' | ConnectionStatus

const PAGE_SIZE = 10

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
        const response = await connectionService.list(page, PAGE_SIZE)
        setConnections(response.connections as unknown as Connection[])
        setTotal(response.total)
      } catch (error) {
        const message = isAPIError(error) ? getErrorMessage(error) : 'Failed to load connections'
        addNotification({ type: 'error', title: 'Error', message })
      } finally {
        setLoading(false)
      }
    }
    load()
  }, [page, addNotification])

  const filtered = statusFilter === 'all'
    ? connections
    : connections.filter(c => c.status === statusFilter)

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

  const statusBadge = (status: ConnectionStatus) => {
    const classes: Record<ConnectionStatus, string> = {
      running: 'bg-green-100 text-green-700',
      stopped: 'bg-yellow-100 text-yellow-700',
      error: 'bg-red-100 text-red-700',
    }
    return (
      <span className={`px-3 py-1 rounded-full text-sm font-medium ${classes[status]}`}>
        {status}
      </span>
    )
  }

  return (
    <div className="space-y-6" data-testid="connections-list">
      <div className="flex items-center justify-between">
        <h1 className="text-3xl font-bold text-gray-900">All Connections</h1>
        <Link
          to="/connections/create"
          className="px-4 py-2 bg-blue-600 text-white rounded-md hover:bg-blue-700 font-medium"
        >
          New Connection
        </Link>
      </div>

      <div className="flex items-center gap-4">
        <label className="text-sm font-medium text-gray-700">Status:</label>
        <select
          value={statusFilter}
          onChange={e => setStatusFilter(e.target.value as StatusFilter)}
          className="px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500 text-sm"
        >
          <option value="all">All</option>
          <option value="running">Running</option>
          <option value="stopped">Stopped</option>
          <option value="error">Error</option>
        </select>
      </div>

      {loading ? (
        <div className="space-y-3">
          {Array.from({ length: 5 }).map((_, i) => (
            <div key={i} className="h-16 bg-gray-100 rounded-lg animate-pulse" />
          ))}
        </div>
      ) : filtered.length === 0 ? (
        <div className="text-center py-16">
          <p className="text-gray-500 text-lg mb-4">No connections yet. Create your first connection.</p>
          <Link
            to="/connections/create"
            className="px-4 py-2 bg-blue-600 text-white rounded-md hover:bg-blue-700 font-medium"
          >
            Create Connection
          </Link>
        </div>
      ) : (
        <div className="bg-white rounded-lg border border-gray-200 overflow-hidden">
          <table className="w-full">
            <thead className="bg-gray-50 border-b border-gray-200">
              <tr>
                <th className="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Name</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Status</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Source</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Destination</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Created</th>
                <th className="px-6 py-3 text-right text-xs font-medium text-gray-500 uppercase tracking-wider">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-200">
              {filtered.map(connection => (
                <tr key={connection.id} className="hover:bg-gray-50">
                  <td className="px-6 py-4">
                    <div>
                      <p className="font-medium text-gray-900">{connection.name}</p>
                      {connection.description && (
                        <p className="text-sm text-gray-500 truncate max-w-xs">{connection.description}</p>
                      )}
                    </div>
                  </td>
                  <td className="px-6 py-4">{statusBadge(connection.status)}</td>
                  <td className="px-6 py-4 text-sm text-gray-600">{connection.source_config.type}</td>
                  <td className="px-6 py-4 text-sm text-gray-600">{connection.destination_config.type}</td>
                  <td className="px-6 py-4 text-sm text-gray-600">
                    {new Date(connection.created_at).toLocaleDateString()}
                  </td>
                  <td className="px-6 py-4 text-right">
                    <div className="flex items-center justify-end gap-2">
                      <button
                        onClick={() => navigate(`/connections/${connection.id}`)}
                        className="text-sm text-blue-600 hover:text-blue-700 font-medium"
                      >
                        View
                      </button>
                      <button
                        onClick={() => navigate(`/connections/${connection.id}/edit`)}
                        className="text-sm text-gray-600 hover:text-gray-700 font-medium"
                      >
                        Edit
                      </button>
                      <button
                        onClick={() => handleDelete(connection)}
                        disabled={actionLoading}
                        className="text-sm text-red-600 hover:text-red-700 font-medium disabled:text-gray-400"
                      >
                        Delete
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {totalPages > 1 && (
        <div className="flex items-center justify-between">
          <button
            onClick={() => setPage(p => Math.max(1, p - 1))}
            disabled={page === 1}
            className="px-4 py-2 text-sm font-medium text-gray-700 bg-white border border-gray-300 rounded-md hover:bg-gray-50 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            Previous
          </button>
          <span className="text-sm text-gray-700">
            Page {page} of {totalPages}
          </span>
          <button
            onClick={() => setPage(p => Math.min(totalPages, p + 1))}
            disabled={page === totalPages}
            className="px-4 py-2 text-sm font-medium text-gray-700 bg-white border border-gray-300 rounded-md hover:bg-gray-50 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            Next
          </button>
        </div>
      )}
    </div>
  )
}
