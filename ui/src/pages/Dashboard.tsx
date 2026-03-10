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
  const { connections = [], setConnections, setLoading, setError } = useConnectionsStore()
  const { showErrorNotification } = useUIStore()
  const runningCount = (connections || []).filter((c) => c.status === 'running').length
  const stoppedCount = (connections || []).filter((c) => c.status === 'stopped').length
  const errorCount = (connections || []).filter((c) => c.status === 'error').length

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
  }, [setConnections, setLoading, setError, showErrorNotification])

  return (
    <div className="space-y-8">
      {/* Page Header */}
      <div style={{ marginBottom: '3rem' }}>
        <h1 style={{ fontSize: '3rem', fontWeight: 'bold', marginBottom: '0.5rem', color: '#0284c7' }}>
          Dashboard
        </h1>
        <p style={{ fontSize: '1.25rem', color: '#666', fontWeight: '500' }}>
          Welcome to VRSky Integration Platform
        </p>
      </div>

      {/* Stats Grid */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-6 py-4">
        {/* Total Connections */}
        <div style={{ padding: '2rem', border: '3px solid #0284c7', borderRadius: '0.75rem', boxShadow: '0 4px 6px rgba(0,0,0,0.1)', backgroundColor: '#fff' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
            <span style={{ fontSize: '0.75rem', fontWeight: 'bold', textTransform: 'uppercase', color: '#999' }}>
              Total Connections
            </span>
            <div style={{ fontSize: '2rem', padding: '0.75rem', backgroundColor: '#e0f2fe', borderRadius: '0.5rem' }}>
              ⚡
            </div>
          </div>
          <div>
            <div style={{ fontSize: '2.25rem', fontWeight: 'bold', color: '#0284c7', marginBottom: '0.5rem' }}>
              {connections.length}
            </div>
            <p style={{ fontSize: '0.75rem', color: '#999' }}>Active integrations</p>
          </div>
        </div>

        {/* Running Connections */}
        <div style={{ padding: '2rem', border: '3px solid #22c55e', borderRadius: '0.75rem', boxShadow: '0 4px 6px rgba(0,0,0,0.1)', backgroundColor: '#fff' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
            <span style={{ fontSize: '0.75rem', fontWeight: 'bold', textTransform: 'uppercase', color: '#999' }}>
              Running
            </span>
            <div style={{ fontSize: '2rem', padding: '0.75rem', backgroundColor: '#dcfce7', borderRadius: '0.5rem' }}>
              😊
            </div>
          </div>
          <div>
            <div style={{ fontSize: '2.25rem', fontWeight: 'bold', color: '#22c55e', marginBottom: '0.5rem' }}>
              {runningCount}
            </div>
            <p style={{ fontSize: '0.75rem', color: '#999' }}>Currently processing</p>
          </div>
        </div>

        {/* Stopped Connections */}
        <div style={{ padding: '2rem', border: '3px solid #f59e0b', borderRadius: '0.75rem', boxShadow: '0 4px 6px rgba(0,0,0,0.1)', backgroundColor: '#fff' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
            <span style={{ fontSize: '0.75rem', fontWeight: 'bold', textTransform: 'uppercase', color: '#999' }}>
              Stopped
            </span>
            <div style={{ fontSize: '2rem', padding: '0.75rem', backgroundColor: '#fef3c7', borderRadius: '0.5rem' }}>
              ⏸
            </div>
          </div>
          <div>
            <div style={{ fontSize: '2.25rem', fontWeight: 'bold', color: '#f59e0b', marginBottom: '0.5rem' }}>
              {stoppedCount}
            </div>
            <p style={{ fontSize: '0.75rem', color: '#999' }}>Paused</p>
          </div>
        </div>

        {/* Error Connections */}
        <div style={{ padding: '2rem', border: '3px solid #ef4444', borderRadius: '0.75rem', boxShadow: '0 4px 6px rgba(0,0,0,0.1)', backgroundColor: '#fff' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
            <span style={{ fontSize: '0.75rem', fontWeight: 'bold', textTransform: 'uppercase', color: '#999' }}>
              Errors
            </span>
            <div style={{ fontSize: '2rem', padding: '0.75rem', backgroundColor: '#fee2e2', borderRadius: '0.5rem' }}>
              ✕
            </div>
          </div>
          <div>
            <div style={{ fontSize: '2.25rem', fontWeight: 'bold', color: '#ef4444', marginBottom: '0.5rem' }}>
              {errorCount}
            </div>
            <p style={{ fontSize: '0.75rem', color: '#999' }}>Need attention</p>
          </div>
        </div>
      </div>

      {/* Recent Connections */}
      <div className="card-elevated p-8 space-y-6 border-t-4 border-primary-500">
        <div className="flex items-center justify-between">
          <h2 className="text-3xl font-bold text-neutral-900 dark:text-neutral-50">
            Recent Connections
          </h2>
          <Link
            to="/connections"
            className="inline-flex items-center gap-2 text-primary-600 dark:text-primary-400 hover:text-primary-700 dark:hover:text-primary-300 font-semibold transition-colors"
          >
            View All →
          </Link>
        </div>

        {connections.length === 0 ? (
          <div className="text-center py-12">
            <div className="inline-flex items-center justify-center w-16 h-16 bg-primary-100 dark:bg-primary-900/30 rounded-full mb-4 text-3xl">
              ⚡
            </div>
            <p className="text-neutral-600 dark:text-neutral-400 mb-4 text-lg">
              No connections yet
            </p>
            <Link to="/connections/create" className="btn-primary">
              Create Your First Connection
            </Link>
          </div>
        ) : (
          <div className="overflow-hidden">
            <div className="space-y-3">
              {connections.slice(0, 5).map((connection) => (
                <Link
                  key={connection.id}
                  to={`/connections/${connection.id}`}
                  className="group flex items-center justify-between p-4 bg-neutral-50 dark:bg-neutral-700/50 rounded-lg hover:bg-neutral-100 dark:hover:bg-neutral-700 transition-colors duration-base"
                >
                  <div className="space-y-1">
                    <h3 className="font-semibold text-neutral-900 dark:text-neutral-50 group-hover:text-primary-600 dark:group-hover:text-primary-400 transition-colors">
                      {connection.name}
                    </h3>
                    <p className="text-sm text-neutral-500 dark:text-neutral-400">
                      {connection.description || 'No description'}
                    </p>
                  </div>
                    <div className="flex items-center gap-3">
                     <span
                       className={`px-3 py-1 rounded-full text-xs font-semibold whitespace-nowrap badge ${
                         connection.status === 'running'
                           ? 'badge-success'
                           : connection.status === 'stopped'
                           ? 'badge-warning'
                           : 'badge-danger'
                       }`}
                     >
                       {connection.status}
                     </span>
                     <span className="text-neutral-400 group-hover:text-neutral-600 dark:group-hover:text-neutral-300 transition-colors">→</span>
                   </div>
                </Link>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
