/**
 * Dashboard Page
 * Home page with connection statistics
 */

import { useEffect } from 'react'
import { Link } from 'react-router-dom'
import { useConnectionsStore } from '../store/connectionsStore'
import { connectionService } from '../services/connectionService'
import { useUIStore } from '../store/uiStore'

export default function Dashboard() {
  const { connections, setConnections, setLoading, setError } = useConnectionsStore()
  const { showErrorNotification } = useUIStore()
  const runningCount = connections.filter((c) => c.status === 'running').length
  const stoppedCount = connections.filter((c) => c.status === 'stopped').length
  const errorCount = connections.filter((c) => c.status === 'error').length

  useEffect(() => {
    const loadConnections = async () => {
      try {
        setLoading(true)
        const response = await connectionService.list()
        setConnections(response.connections as unknown as typeof connections)
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Failed to load connections'
        showErrorNotification('Error', message)
        setError(message)
      } finally {
        setLoading(false)
      }
    }

    loadConnections()
  }, [])

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-3xl font-bold text-gray-900">Dashboard</h1>
        <p className="mt-2 text-gray-600">Welcome to VRSky Integration Platform</p>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
        <div className="card p-6">
          <div className="text-sm font-medium text-gray-600">Total Connections</div>
          <div className="mt-2 text-3xl font-bold text-gray-900">{connections.length}</div>
        </div>

        <div className="card p-6 border-l-4 border-green-500">
          <div className="text-sm font-medium text-gray-600">Running</div>
          <div className="mt-2 text-3xl font-bold text-green-600">{runningCount}</div>
        </div>

        <div className="card p-6 border-l-4 border-yellow-500">
          <div className="text-sm font-medium text-gray-600">Stopped</div>
          <div className="mt-2 text-3xl font-bold text-yellow-600">{stoppedCount}</div>
        </div>

        <div className="card p-6 border-l-4 border-red-500">
          <div className="text-sm font-medium text-gray-600">Errors</div>
          <div className="mt-2 text-3xl font-bold text-red-600">{errorCount}</div>
        </div>
      </div>

      <div className="card p-6">
        <h2 className="text-lg font-bold text-gray-900 mb-4">Recent Connections</h2>
        {connections.length === 0 ? (
          <div className="text-center py-8">
            <p className="text-gray-600 mb-4">No connections yet</p>
            <Link to="/connections/create" className="btn-primary">
              Create Your First Connection
            </Link>
          </div>
        ) : (
          <div className="space-y-2">
            {connections.slice(0, 5).map((connection) => (
              <div key={connection.id} className="flex items-center justify-between p-4 bg-gray-50 rounded-lg">
                <div>
                  <h3 className="font-medium text-gray-900">{connection.name}</h3>
                  <p className="text-sm text-gray-600">{connection.description}</p>
                </div>
                <span className={`px-3 py-1 rounded-full text-sm font-medium ${
                  connection.status === 'running'
                    ? 'bg-green-100 text-green-700'
                    : connection.status === 'stopped'
                    ? 'bg-yellow-100 text-yellow-700'
                    : 'bg-red-100 text-red-700'
                }`}>
                  {connection.status}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
