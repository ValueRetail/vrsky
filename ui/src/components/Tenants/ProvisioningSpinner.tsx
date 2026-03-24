/**
 * ProvisioningSpinner
 * Shows progress while a tenant's NATS instance is being provisioned
 */

import { useTenantStatus } from '@/hooks/useTenantStatus'

interface ProvisioningSpinnerProps {
  tenantId: string
  onComplete?: () => void
}

export default function ProvisioningSpinner({ tenantId, onComplete }: ProvisioningSpinnerProps) {
  const status = useTenantStatus(tenantId)

  const progress = status?.progress ?? 0
  const step = status?.current_step ?? 'Initializing...'
  const isActive = status?.status === 'active'
  const isFailed = status?.status === 'failed'

  if (isActive) {
    if (onComplete) {
      // Slight delay so the user sees "Ready" before closing
      setTimeout(onComplete, 1000)
    }
    return (
      <div className="text-center py-6">
        <div className="text-2xl mb-2">&#10003;</div>
        <p className="text-sm font-medium text-green-600 dark:text-green-400">Workspace ready!</p>
      </div>
    )
  }

  if (isFailed) {
    return (
      <div className="text-center py-6">
        <div className="text-2xl mb-2">&#10007;</div>
        <p className="text-sm font-medium text-red-600 dark:text-red-400">
          Setup failed: {status?.error || 'Unknown error'}
        </p>
      </div>
    )
  }

  return (
    <div className="text-center py-6">
      <div className="animate-spin w-8 h-8 border-4 border-primary-500 border-t-transparent rounded-full mx-auto mb-4" />
      <div className="w-full bg-neutral-200 dark:bg-neutral-700 rounded-full h-2 mb-3">
        <div
          className="bg-primary-500 h-2 rounded-full transition-all duration-500"
          style={{ width: `${progress}%` }}
        />
      </div>
      <p className="text-sm text-neutral-600 dark:text-neutral-400">{step}</p>
      <p className="text-xs text-neutral-500 dark:text-neutral-500 mt-1">{progress}% complete</p>
    </div>
  )
}
