import { useState } from 'react'
import { useAuthStore } from '../../store/authStore'
import CreateTenantModal from './CreateTenantModal'

export default function TenantSelector() {
  const { tenants, currentTenant, switchTenant } = useAuthStore()
  const [showCreateModal, setShowCreateModal] = useState(false)

  if (tenants.length === 0) return null

  return (
    <div className="flex items-center gap-2">
      <select
        value={currentTenant?.id || ''}
        onChange={(e) => {
          const tenant = tenants.find(t => t.id === e.target.value)
          if (tenant && tenant.status === 'active') switchTenant(tenant)
        }}
        className="px-2 py-1 text-sm border border-neutral-300 dark:border-neutral-600 rounded-md bg-white dark:bg-neutral-800 text-neutral-900 dark:text-neutral-100 focus:outline-none focus:ring-1 focus:ring-primary-500"
        aria-label="Select workspace"
      >
        {tenants.map(t => (
          <option key={t.id} value={t.id} disabled={t.status !== 'active'}>
            {t.name} ({t.user_role}){t.status === 'provisioning' ? ' - setting up...' : t.status === 'failed' ? ' - failed' : ''}
          </option>
        ))}
      </select>
      <button
        onClick={() => setShowCreateModal(true)}
        className="px-2 py-1 text-sm font-medium text-primary-600 dark:text-primary-400 hover:bg-primary-50 dark:hover:bg-primary-900/20 rounded transition-colors"
        title="Create new workspace"
      >
        +
      </button>
      <CreateTenantModal isOpen={showCreateModal} onClose={() => setShowCreateModal(false)} />
    </div>
  )
}
