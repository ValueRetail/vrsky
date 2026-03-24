import { useState, useEffect } from 'react'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import * as tenantDataService from '@/services/tenantDataService'
import type { DataConnectionRequest } from '@/types/models'

export default function ConnectionRequestsPage() {
  const { currentTenant, tenants } = useAuthStore()
  const { addNotification, showConfirmDialog, hideConfirmDialog } = useUIStore()
  const [tab, setTab] = useState<'incoming' | 'outgoing'>('incoming')
  const [incoming, setIncoming] = useState<DataConnectionRequest[]>([])
  const [outgoing, setOutgoing] = useState<DataConnectionRequest[]>([])
  const [loading, setLoading] = useState(true)
  const [showCreateForm, setShowCreateForm] = useState(false)
  const [targetTenantId, setTargetTenantId] = useState('')
  const [permissionType, setPermissionType] = useState('both')
  const [message, setMessage] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const otherTenants = tenants.filter(t => t.id !== currentTenant?.id)

  const handleCreateRequest = async () => {
    if (!currentTenant || !targetTenantId) return
    setSubmitting(true)
    try {
      const isOwnWorkspace = otherTenants.some(t => t.id === targetTenantId)
      const req = await tenantDataService.createConnectionRequest(currentTenant.id, {
        ...(isOwnWorkspace ? { target_tenant_id: targetTenantId } : { target_api_key: targetTenantId }),
        permission_type: permissionType,
        message: message || undefined,
      })
      addNotification({ id: Date.now().toString(), type: 'success', title: 'Sent', message: 'Connection request sent' })
      setOutgoing(prev => [req, ...prev])
      setShowCreateForm(false)
      setTargetTenantId('')
      setMessage('')
      setTab('outgoing')
    } catch {
      addNotification({ id: Date.now().toString(), type: 'error', title: 'Error', message: 'Failed to send connection request' })
    } finally {
      setSubmitting(false)
    }
  }

  useEffect(() => {
    if (!currentTenant) { setLoading(false); return }
    setLoading(true)
    Promise.all([
      tenantDataService.listIncomingRequests(currentTenant.id),
      tenantDataService.listOutgoingRequests(currentTenant.id),
    ])
      .then(([inc, out]) => { setIncoming(inc); setOutgoing(out) })
      .catch(() => addNotification({ id: Date.now().toString(), type: 'error', title: 'Error', message: 'Failed to load connection requests' }))
      .finally(() => setLoading(false))
  }, [currentTenant, addNotification])

  const handleApprove = (requestId: string) => {
    if (!currentTenant) return
    showConfirmDialog({
      title: 'Approve Connection Request',
      message: 'This will allow the requesting tenant to share data with your workspace. You can revoke access later.',
      confirmLabel: 'Approve',
      onConfirm: async () => {
        hideConfirmDialog()
        try {
          await tenantDataService.approveRequest(currentTenant.id, requestId)
          addNotification({ id: Date.now().toString(), type: 'success', title: 'Approved', message: 'Connection request approved' })
          setIncoming(prev => prev.filter(r => r.id !== requestId))
        } catch {
          addNotification({ id: Date.now().toString(), type: 'error', title: 'Error', message: 'Failed to approve request' })
        }
      },
    })
  }

  const handleDeny = (requestId: string) => {
    if (!currentTenant) return
    showConfirmDialog({
      title: 'Deny Connection Request',
      message: 'Are you sure you want to deny this connection request?',
      confirmLabel: 'Deny',
      destructive: true,
      onConfirm: async () => {
        hideConfirmDialog()
        try {
          await tenantDataService.denyRequest(currentTenant.id, requestId)
          addNotification({ id: Date.now().toString(), type: 'success', title: 'Denied', message: 'Connection request denied' })
          setIncoming(prev => prev.filter(r => r.id !== requestId))
        } catch {
          addNotification({ id: Date.now().toString(), type: 'error', title: 'Error', message: 'Failed to deny request' })
        }
      },
    })
  }

  const requests = tab === 'incoming' ? incoming : outgoing

  return (
    <div className="p-6 max-w-4xl mx-auto">
      <h1 className="text-2xl font-bold text-neutral-900 dark:text-neutral-100 mb-6">Connection Requests</h1>

      <div className="flex items-center gap-2 mb-6">
        <button
          onClick={() => setTab('incoming')}
          className={`px-4 py-2 text-sm font-medium rounded-md transition-colors ${tab === 'incoming' ? 'bg-primary-600 text-white' : 'bg-neutral-100 dark:bg-neutral-700 text-neutral-700 dark:text-neutral-300'}`}
        >
          Incoming {incoming.filter(r => r.status === 'pending').length > 0 && `(${incoming.filter(r => r.status === 'pending').length})`}
        </button>
        <button
          onClick={() => setTab('outgoing')}
          className={`px-4 py-2 text-sm font-medium rounded-md transition-colors ${tab === 'outgoing' ? 'bg-primary-600 text-white' : 'bg-neutral-100 dark:bg-neutral-700 text-neutral-700 dark:text-neutral-300'}`}
        >
          Outgoing
        </button>
        <div className="flex-1" />
        <button
          onClick={() => setShowCreateForm(!showCreateForm)}
          className="px-4 py-2 text-sm font-medium text-white bg-primary-600 hover:bg-primary-700 rounded-md transition-colors"
        >
          {showCreateForm ? 'Cancel' : 'New Request'}
        </button>
      </div>

      {showCreateForm && (
        <div className="mb-6 p-4 bg-white dark:bg-neutral-800 border border-neutral-200 dark:border-neutral-700 rounded-lg space-y-4">
          <h3 className="text-sm font-medium text-neutral-900 dark:text-neutral-100">Send Connection Request</h3>
          <div>
            <label className="block text-xs text-neutral-500 dark:text-neutral-400 mb-1">Target Workspace</label>
            {otherTenants.length > 0 && (
              <select
                value={otherTenants.some(t => t.id === targetTenantId) ? targetTenantId : ''}
                onChange={e => setTargetTenantId(e.target.value)}
                className="w-full px-3 py-2 text-sm border border-neutral-300 dark:border-neutral-600 rounded-md bg-white dark:bg-neutral-900 text-neutral-900 dark:text-neutral-100 mb-2"
              >
                <option value="">Select your own workspace...</option>
                {otherTenants.map(t => (
                  <option key={t.id} value={t.id}>{t.name}</option>
                ))}
              </select>
            )}
            <label className="block text-xs text-neutral-500 dark:text-neutral-400 mb-1">{otherTenants.length > 0 ? 'Or paste an API key' : 'Target API Key'}</label>
            <input
              type="text"
              value={otherTenants.some(t => t.id === targetTenantId) ? '' : targetTenantId}
              onChange={e => setTargetTenantId(e.target.value)}
              placeholder="Paste the target workspace's API key"
              className="w-full px-3 py-2 text-sm border border-neutral-300 dark:border-neutral-600 rounded-md bg-white dark:bg-neutral-900 text-neutral-900 dark:text-neutral-100"
            />
          </div>
          <div>
            <label className="block text-xs text-neutral-500 dark:text-neutral-400 mb-1">Permission Type</label>
            <select
              value={permissionType}
              onChange={e => setPermissionType(e.target.value)}
              className="w-full px-3 py-2 text-sm border border-neutral-300 dark:border-neutral-600 rounded-md bg-white dark:bg-neutral-900 text-neutral-900 dark:text-neutral-100"
            >
              <option value="both">Both (send & receive)</option>
              <option value="send">Send only</option>
              <option value="receive">Receive only</option>
            </select>
          </div>
          <div>
            <label className="block text-xs text-neutral-500 dark:text-neutral-400 mb-1">Message (optional)</label>
            <input
              type="text"
              value={message}
              onChange={e => setMessage(e.target.value)}
              placeholder="Why are you requesting this connection?"
              className="w-full px-3 py-2 text-sm border border-neutral-300 dark:border-neutral-600 rounded-md bg-white dark:bg-neutral-900 text-neutral-900 dark:text-neutral-100"
            />
          </div>
          <button
            onClick={handleCreateRequest}
            disabled={!targetTenantId || submitting}
            className="px-4 py-2 text-sm font-medium text-white bg-primary-600 hover:bg-primary-700 disabled:opacity-50 rounded-md transition-colors"
          >
            {submitting ? 'Sending...' : 'Send Request'}
          </button>
        </div>
      )}

      {loading ? (
        <p className="text-neutral-500 dark:text-neutral-400">Loading...</p>
      ) : requests.length === 0 ? (
        <p className="text-neutral-500 dark:text-neutral-400">No {tab} requests</p>
      ) : (
        <div className="space-y-3">
          {requests.map(req => (
            <div key={req.id} className="p-4 bg-white dark:bg-neutral-800 border border-neutral-200 dark:border-neutral-700 rounded-lg">
              <div className="flex items-center justify-between">
                <div>
                  <p className="text-sm font-medium text-neutral-900 dark:text-neutral-100">
                    {tab === 'incoming' ? req.requester_tenant_name || req.requester_tenant_id : req.target_tenant_name || req.target_tenant_id}
                  </p>
                  <p className="text-xs text-neutral-500 dark:text-neutral-400 mt-1">
                    Permission: <span className="font-medium">{req.permission_type}</span>
                    {req.message && <> &middot; {req.message}</>}
                  </p>
                  <p className="text-xs text-neutral-400 dark:text-neutral-500 mt-1">
                    Status: <span className={`font-medium ${req.status === 'pending' ? 'text-yellow-600' : req.status === 'approved' ? 'text-green-600' : 'text-red-600'}`}>{req.status}</span>
                    {' '}&middot; {new Date(req.created_at).toLocaleDateString()}
                  </p>
                </div>
                {tab === 'incoming' && req.status === 'pending' && (
                  <div className="flex gap-2">
                    <button onClick={() => handleApprove(req.id)} className="px-3 py-1 text-sm font-medium text-white bg-green-600 hover:bg-green-700 rounded-md transition-colors">Approve</button>
                    <button onClick={() => handleDeny(req.id)} className="px-3 py-1 text-sm font-medium text-white bg-red-600 hover:bg-red-700 rounded-md transition-colors">Deny</button>
                  </div>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
