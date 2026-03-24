/**
 * CreateTenantModal
 * Modal for creating a new workspace with provisioning progress
 */

import { useState } from 'react'
import { useAuthStore } from '@/store/authStore'
import ProvisioningSpinner from './ProvisioningSpinner'
import * as authService from '@/services/authService'
import { config } from '@/config/env'

interface CreateTenantModalProps {
  isOpen: boolean
  onClose: () => void
}

export default function CreateTenantModal({ isOpen, onClose }: CreateTenantModalProps) {
  const { checkAuth } = useAuthStore()
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [provisioningTenantId, setProvisioningTenantId] = useState<string | null>(null)

  if (!isOpen) return null

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)

    if (!name || name.trim().length < 3) {
      setError('Workspace name must be at least 3 characters')
      return
    }

    setIsSubmitting(true)
    try {
      const token = authService.getSessionToken()
      const res = await fetch(`${config.apiUrl}/api/v1/tenants`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`,
        },
        body: JSON.stringify({ name: name.trim() }),
      })

      if (!res.ok) {
        const data = await res.json()
        setError(data.message || 'Failed to create workspace')
        setIsSubmitting(false)
        return
      }

      const data = await res.json()
      const tenantId = data.tenant?.id
      if (tenantId) {
        setProvisioningTenantId(tenantId)
      } else {
        handleComplete()
      }
    } catch {
      setError('Failed to create workspace')
      setIsSubmitting(false)
    }
  }

  const handleComplete = () => {
    setProvisioningTenantId(null)
    setName('')
    setIsSubmitting(false)
    checkAuth()
    onClose()
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="bg-white dark:bg-neutral-800 rounded-lg shadow-xl w-full max-w-md mx-4 p-6">
        <h3 className="text-lg font-semibold text-neutral-900 dark:text-neutral-100 mb-4">
          {provisioningTenantId ? 'Setting up workspace...' : 'Create new workspace'}
        </h3>

        {provisioningTenantId ? (
          <ProvisioningSpinner tenantId={provisioningTenantId} onComplete={handleComplete} />
        ) : (
          <form onSubmit={handleSubmit}>
            {error && (
              <div className="mb-4 rounded-md bg-red-50 dark:bg-red-900/20 p-3 border border-red-200 dark:border-red-800">
                <p className="text-sm text-red-700 dark:text-red-400">{error}</p>
              </div>
            )}

            <div className="mb-4">
              <label htmlFor="tenantName" className="block text-sm font-medium text-neutral-700 dark:text-neutral-300 mb-1">
                Workspace name
              </label>
              <input
                id="tenantName"
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                className="block w-full px-3 py-2 border border-neutral-300 dark:border-neutral-600 rounded-md shadow-sm bg-white dark:bg-neutral-700 text-neutral-900 dark:text-neutral-100 focus:outline-none focus:ring-primary-500 focus:border-primary-500"
                placeholder="e.g., Acme Corp"
                autoFocus
              />
            </div>

            <div className="flex gap-3 justify-end">
              <button
                type="button"
                onClick={onClose}
                className="px-4 py-2 text-sm font-medium text-neutral-700 dark:text-neutral-300 bg-white dark:bg-neutral-700 border border-neutral-300 dark:border-neutral-600 rounded-md hover:bg-neutral-50 dark:hover:bg-neutral-600 transition-colors"
              >
                Cancel
              </button>
              <button
                type="submit"
                disabled={isSubmitting}
                className="px-4 py-2 text-sm font-medium text-white bg-primary-600 hover:bg-primary-700 rounded-md disabled:opacity-50 transition-colors"
              >
                {isSubmitting ? 'Creating...' : 'Create workspace'}
              </button>
            </div>
          </form>
        )}
      </div>
    </div>
  )
}
