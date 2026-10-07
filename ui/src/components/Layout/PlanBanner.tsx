/**
 * Trial / suspension banner (plans/paid-plans.md). Shown under the header for a
 * workspace on trial (counting down) or suspended (pipelines stopped); nothing
 * for a paid one. The Plan page is where both lead.
 */

import { Link } from 'react-router-dom'
import { useAuthStore } from '@/store/authStore'
import { daysLeft } from '@/utils/plan'

export default function PlanBanner() {
  const tenant = useAuthStore((s) => s.currentTenant)
  if (!tenant || tenant.billing_status === 'paid' || !tenant.billing_status) return null

  if (tenant.billing_status === 'suspended') {
    return (
      <div role="status" className="bg-error-50 dark:bg-error-900/30 border-b border-error-200 dark:border-error-800 px-6 py-2 text-sm text-error-800 dark:text-error-200 flex items-center justify-between gap-4">
        <span>
          <span className="font-semibold">Your trial has ended.</span> Pipelines are stopped; everything you built is kept.
        </span>
        <Link to="/settings/plan" className="font-semibold underline whitespace-nowrap">Request a plan</Link>
      </div>
    )
  }

  const days = daysLeft(tenant.trial_ends_at)
  return (
    <div role="status" className="bg-amber-50 dark:bg-amber-900/20 border-b border-amber-200 dark:border-amber-800 px-6 py-2 text-sm text-amber-900 dark:text-amber-200 flex items-center justify-between gap-4">
      <span>
        <span className="font-semibold">Trial</span>
        {days !== null && <> · {days === 0 ? 'ends today' : `${days} day${days === 1 ? '' : 's'} left`}</>}
      </span>
      <Link to="/settings/plan" className="font-semibold underline whitespace-nowrap">Choose a plan</Link>
    </div>
  )
}
