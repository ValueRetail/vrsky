import { useState, useEffect } from 'react'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import * as tenantDataService from '@/services/tenantDataService'
import type { TenantAPIKey } from '@/types/models'

export default function ApiKeyPage() {
  const { currentTenant } = useAuthStore()
  const { addNotification, showConfirmDialog, hideConfirmDialog } = useUIStore()
  const [apiKey, setApiKey] = useState<TenantAPIKey | null>(null)
  const [rawKey, setRawKey] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (!currentTenant) { setLoading(false); return }
    setLoading(true)
    tenantDataService.getApiKey(currentTenant.id)
      .then(setApiKey)
      .catch(() => setApiKey(null))
      .finally(() => setLoading(false))
  }, [currentTenant])

  const handleRotate = () => {
    if (!currentTenant) return
    showConfirmDialog({
      title: 'Rotate API Key',
      message: 'This will invalidate the current key immediately. Any services using the old key will stop working. The new key will only be shown once.',
      confirmLabel: 'Rotate Key',
      destructive: true,
      onConfirm: async () => {
        hideConfirmDialog()
        try {
          const result = await tenantDataService.rotateApiKey(currentTenant.id)
          setApiKey(result)
          setRawKey(result.raw_key)
          addNotification({ id: Date.now().toString(), type: 'success', title: 'Key Rotated', message: 'New API key generated. Copy it now — it will not be shown again.' })
        } catch {
          addNotification({ id: Date.now().toString(), type: 'error', title: 'Error', message: 'Failed to rotate API key' })
        }
      },
    })
  }

  const handleCopy = () => {
    if (rawKey) {
      navigator.clipboard.writeText(rawKey)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }
  }

  return (
    <div className="p-6 max-w-2xl mx-auto">
      <h1 className="text-2xl font-bold text-neutral-900 dark:text-neutral-100 mb-6">API Key</h1>
      <p className="text-sm text-neutral-600 dark:text-neutral-400 mb-6">
        Your API key is used by other tenants to send data to your workspace. Keep it secure.
      </p>

      {loading ? (
        <p className="text-neutral-500 dark:text-neutral-400">Loading...</p>
      ) : (
        <div className="bg-white dark:bg-neutral-800 border border-neutral-200 dark:border-neutral-700 rounded-lg p-6">
          {apiKey ? (
            <div className="space-y-4">
              <div className="grid grid-cols-2 gap-4 text-sm">
                <div>
                  <span className="text-neutral-500 dark:text-neutral-400">Status</span>
                  <p className={`font-medium ${apiKey.is_active ? 'text-green-600' : 'text-red-600'}`}>
                    {apiKey.is_active ? 'Active' : 'Inactive'}
                  </p>
                </div>
                <div>
                  <span className="text-neutral-500 dark:text-neutral-400">Created</span>
                  <p className="font-medium text-neutral-900 dark:text-neutral-100">{new Date(apiKey.created_at).toLocaleDateString()}</p>
                </div>
                {apiKey.rotated_at && (
                  <div>
                    <span className="text-neutral-500 dark:text-neutral-400">Last Rotated</span>
                    <p className="font-medium text-neutral-900 dark:text-neutral-100">{new Date(apiKey.rotated_at).toLocaleDateString()}</p>
                  </div>
                )}
              </div>

              {rawKey && (
                <div className="mt-4 p-4 bg-yellow-50 dark:bg-yellow-900/20 border border-yellow-200 dark:border-yellow-700 rounded-lg">
                  <p className="text-xs font-medium text-yellow-800 dark:text-yellow-300 mb-2">
                    Copy this key now. It will not be shown again.
                  </p>
                  <div className="flex items-center gap-2">
                    <code className="flex-1 text-xs bg-neutral-100 dark:bg-neutral-900 p-2 rounded font-mono break-all">
                      {rawKey}
                    </code>
                    <button
                      onClick={handleCopy}
                      className="px-3 py-1 text-sm font-medium bg-neutral-200 dark:bg-neutral-700 rounded hover:bg-neutral-300 dark:hover:bg-neutral-600 transition-colors"
                    >
                      {copied ? 'Copied!' : 'Copy'}
                    </button>
                  </div>
                </div>
              )}
            </div>
          ) : (
            <p className="text-sm text-neutral-500 dark:text-neutral-400">No API key generated yet.</p>
          )}

          <button
            onClick={handleRotate}
            className="mt-6 px-4 py-2 text-sm font-medium text-white bg-primary-600 hover:bg-primary-700 rounded-md transition-colors"
          >
            {apiKey ? 'Rotate Key' : 'Generate API Key'}
          </button>
        </div>
      )}
    </div>
  )
}
