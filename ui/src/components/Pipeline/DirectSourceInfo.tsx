import { useAuthStore } from '../../store/authStore'
import { DIRECT_SOURCE_STREAM, directSubject } from './directSource'

// DirectSourceInfo: the panel of a `direct` source. It has no settings; what
// the user needs is where the publisher writes, so it shows the stream, the
// subject with the real workspace and connection ids once they exist, and the
// envelope fields VRSky routes on.
export default function DirectSourceInfo({ deployedConnectionId }: { deployedConnectionId?: string }) {
  const currentTenant = useAuthStore((s) => s.currentTenant)
  const subject = directSubject(currentTenant?.id, deployedConnectionId)

  return (
    <div className="space-y-3">
      <p className="text-xs text-neutral-500 dark:text-neutral-400">
        Another system writes finished VRSky envelopes into this pipeline itself. VRSky starts nothing for this
        source and has nothing to configure; it processes what arrives on the subject below.
      </p>

      <div className="p-3 bg-neutral-50 dark:bg-neutral-800/60 border border-neutral-200 dark:border-neutral-700 rounded-md space-y-2">
        <div>
          <p className="text-xs font-medium text-neutral-600 dark:text-neutral-300 mb-1">Stream</p>
          <code className="text-xs font-mono">{DIRECT_SOURCE_STREAM}</code>
        </div>
        <div>
          <p className="text-xs font-medium text-neutral-600 dark:text-neutral-300 mb-1">Subject</p>
          <div className="flex items-center gap-2">
            <code data-testid="direct-subject" className="flex-1 text-xs font-mono break-all">{subject}</code>
            {deployedConnectionId && currentTenant?.id && (
              <button
                onClick={() => { navigator.clipboard.writeText(subject) }}
                className="px-2 py-1 text-xs bg-neutral-700 text-white rounded hover:bg-neutral-800 whitespace-nowrap"
              >
                Copy
              </button>
            )}
          </div>
        </div>
        <p className="text-xs text-neutral-600 dark:text-neutral-300">
          In each envelope, <code className="font-mono">tenant_id</code> is this workspace&apos;s id
          {currentTenant?.id && <> (<code className="font-mono break-all">{currentTenant.id}</code>)</>} and{' '}
          <code className="font-mono">integration_id</code> is the connection id
          {deployedConnectionId && <> (<code className="font-mono break-all">{deployedConnectionId}</code>)</>}.
          Stored secrets are resolved under the envelope&apos;s tenant.
        </p>
      </div>

      <p className="text-xs text-amber-700 dark:text-amber-300">
        Stopping this pipeline does not stop deliveries: messages the publisher sends are still processed. Stop the
        publisher to stop the flow.
      </p>

      {!deployedConnectionId && (
        <p className="text-xs text-neutral-600 dark:text-neutral-300">
          Deploy the pipeline to get its connection id.
        </p>
      )}
    </div>
  )
}
