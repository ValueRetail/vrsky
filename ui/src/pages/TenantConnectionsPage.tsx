import { useState, useEffect } from 'react'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import * as tenantDataService from '@/services/tenantDataService'
import type { TenantDataConnection } from '@/types/models'

export default function TenantConnectionsPage() {
  const { currentTenant } = useAuthStore()
  const { addNotification, showConfirmDialog, hideConfirmDialog } = useUIStore()
  const [connections, setConnections] = useState<TenantDataConnection[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    if (!currentTenant) { setLoading(false); return }
    setLoading(true)
    tenantDataService.listDataConnections(currentTenant.id)
      .then(setConnections)
      .catch(() => addNotification({ id: Date.now().toString(), type: 'error', title: 'Error', message: 'Failed to load data connections' }))
      .finally(() => setLoading(false))
  }, [currentTenant, addNotification])

  const handleRevoke = (connectionId: string) => {
    if (!currentTenant) return
    showConfirmDialog({
      title: 'Revoke Data Connection',
      message: 'This will immediately stop all data sharing and pause any pipeline flows using this connection. This action cannot be undone.',
      confirmLabel: 'Revoke',
      destructive: true,
      onConfirm: async () => {
        hideConfirmDialog()
        try {
          await tenantDataService.revokeDataConnection(currentTenant.id, connectionId)
          addNotification({ id: Date.now().toString(), type: 'success', title: 'Revoked', message: 'Data connection revoked' })
          setConnections(prev => prev.map(c => c.id === connectionId ? { ...c, status: 'revoked' as const } : c))
        } catch {
          addNotification({ id: Date.now().toString(), type: 'error', title: 'Error', message: 'Failed to revoke connection' })
        }
      },
    })
  }

  const statusBadge = (status: string) => {
    const colors = {
      active: 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400',
      paused: 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400',
      revoked: 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400',
    }
    return (
      <span className={`px-2 py-0.5 text-xs font-medium rounded-full ${colors[status as keyof typeof colors] || 'bg-neutral-100 text-neutral-600'}`}>
        {status}
      </span>
    )
  }

  return (
    <div className="p-6 max-w-4xl mx-auto">
      <h1 className="text-2xl font-bold text-neutral-900 dark:text-neutral-100 mb-6">Data Connections</h1>

      {loading ? (
        <p className="text-neutral-500 dark:text-neutral-400">Loading...</p>
      ) : connections.length === 0 ? (
        <p className="text-neutral-500 dark:text-neutral-400">No data connections yet</p>
      ) : (
        <div className="space-y-3">
          {connections.map(conn => (
            <div key={conn.id} className="p-4 bg-white dark:bg-neutral-800 border border-neutral-200 dark:border-neutral-700 rounded-lg">
              <div className="flex items-center justify-between">
                <div>
                  <div className="flex items-center gap-2">
                    <p className="text-sm font-medium text-neutral-900 dark:text-neutral-100">
                      {conn.requester_tenant_id === currentTenant?.id ? `To: ${conn.target_tenant_id}` : `From: ${conn.requester_tenant_id}`}
                    </p>
                    {statusBadge(conn.status)}
                  </div>
                  <p className="text-xs text-neutral-500 dark:text-neutral-400 mt-1">
                    Permission: {conn.permission_type} &middot; Rate limit: {conn.rate_limit_per_hour}/hr
                  </p>
                  <p className="text-xs text-neutral-400 dark:text-neutral-500 mt-1">
                    Created: {new Date(conn.created_at).toLocaleDateString()}
                    {conn.revoked_at && <> &middot; Revoked: {new Date(conn.revoked_at).toLocaleDateString()}</>}
                  </p>
                </div>
                {conn.status === 'active' && (
                  <button
                    onClick={() => handleRevoke(conn.id)}
                    className="px-3 py-1 text-sm font-medium text-red-600 dark:text-red-400 border border-red-300 dark:border-red-700 rounded-md hover:bg-red-50 dark:hover:bg-red-900/20 transition-colors"
                  >
                    Revoke
                  </button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
