import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ReactFlowProvider } from 'reactflow'
import PipelineBuilder from './PipelineBuilder'
import { connectionService } from '../services/connectionService'
import { useAuthStore } from '../store/authStore'
import { isOnboarded, markOnboarded } from '../onboarding/state'

export default function PipelineBuilderPage() {
  const navigate = useNavigate()
  const currentTenant = useAuthStore((s) => s.currentTenant)
  const [decided, setDecided] = useState(false)

  // First-login onboarding (#93): a tenant with zero connections that hasn't
  // been onboarded is redirected to the template wizard. Checked once per
  // tenant; tenants that already have connections are flagged so we never look
  // again. Failures fall through to the normal builder.
  useEffect(() => {
    const tenantId = currentTenant?.id
    if (!tenantId || isOnboarded(tenantId)) return
    let cancelled = false
    ;(async () => {
      try {
        // The service maps the API's { data, total } list body (#450); reading
        // the body here by hand got `total` wrong and sent workspaces that
        // have connections to the wizard.
        const { total } = await connectionService.list(1, 1)
        if (cancelled) return
        if (total === 0) {
          navigate('/welcome', { replace: true })
          return
        }
        markOnboarded(tenantId)
      } catch {
        /* ignore — show the builder */
      }
      if (!cancelled) setDecided(true)
    })()
    return () => {
      cancelled = true
    }
  }, [currentTenant?.id, navigate])

  // Hold the (blank) canvas back until the redirect decision is made, so a
  // first-time user doesn't see the empty builder flash before the wizard.
  const tenantId = currentTenant?.id
  if (tenantId && !isOnboarded(tenantId) && !decided) {
    return null
  }

  return (
    <ReactFlowProvider>
      <PipelineBuilder />
    </ReactFlowProvider>
  )
}
