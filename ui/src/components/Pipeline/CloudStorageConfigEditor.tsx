import { useEffect } from 'react'
import SecretInput from './SecretInput'
import TestConnectionButton from './TestConnectionButton'
import { StyledInput, StyledSelect } from './StyledFields'
import { DEFAULT_POLL_INTERVAL_SECONDS } from './pollInterval'

// CloudStorageConfigEditor configures the single cloud-storage connector
// (Amazon S3 / Azure Blob / GCS) for both consumer and producer roles. The
// provider picker swaps the credential fields. PR1 ships S3 (and S3-compatible
// stores such as MinIO via a custom endpoint); Azure/GCS land in PR2.
export default function CloudStorageConfigEditor({
  config,
  setConfig,
  role,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
  role: 'consumer' | 'producer'
}) {
  const cs = (config.cloud_storage as Record<string, unknown>) || {}
  const update = (patch: Record<string, unknown>) =>
    setConfig({ ...config, cloud_storage: { ...cs, ...patch } })

  const provider = (cs.provider as string) || 's3'
  const afterAction = (cs.after_action as string) || 'none'
  const mode = (cs.mode as string) || 'poll'

  // Same as the SFTP panel (#589): a source in poll mode saves the 60 the
  // field shows, because a missing value means "poll once". Event mode has no
  // interval, so it gets none until the mode goes back to poll.
  const pollUnset = role === 'consumer' && mode !== 'event' && cs.poll_interval_seconds === undefined
  useEffect(() => {
    if (pollUnset) update({ poll_interval_seconds: DEFAULT_POLL_INTERVAL_SECONDS })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pollUnset])

  return (
    <div className="space-y-3">
      <StyledSelect
        label="Provider"
        value={provider}
        onChange={(v) => update({ provider: v })}
        options={[
          { value: 's3', label: 'Amazon S3 (or S3-compatible)' },
          { value: 'azure', label: 'Azure Blob Storage' },
          { value: 'gcs', label: 'Google Cloud Storage' },
        ]}
      />

      <StyledInput
        label={provider === 'azure' ? 'Container' : 'Bucket'}
        placeholder={provider === 'azure' ? 'my-container' : 'my-bucket'}
        value={(cs.bucket as string) || ''}
        onChange={(v) => update({ bucket: v })}
      />
      <StyledInput
        label="Prefix (optional)"
        placeholder={role === 'consumer' ? 'incoming/' : 'outgoing/'}
        value={(cs.prefix as string) || ''}
        onChange={(v) => update({ prefix: v })}
      />

      {provider === 's3' && (
        <>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
            <StyledInput
              label="Region"
              placeholder="us-east-1"
              value={(cs.region as string) || ''}
              onChange={(v) => update({ region: v })}
            />
            <StyledInput
              label="Endpoint (optional, for MinIO)"
              placeholder="http://minio:9000"
              value={(cs.endpoint as string) || ''}
              onChange={(v) => update({ endpoint: v })}
            />
          </div>
          <StyledInput
            label="Access key ID"
            placeholder="AKIA… (or MinIO user)"
            value={(cs.access_key_id as string) || ''}
            onChange={(v) => update({ access_key_id: v })}
          />
          <div>
            <SecretInput
              label="Secret access key"
              placeholder="S3 secret access key"
              field="secret_access_key"
              config={cs}
              defaultSecretName="s3-secret-access-key"
              onChange={(patch) => update(patch)}
            />
          </div>
        </>
      )}

      {provider === 'azure' && (
        <>
          <div>
            <SecretInput
              label="Connection string"
              placeholder="DefaultEndpointsProtocol=…;AccountName=…;AccountKey=…"
              field="connection_string"
              config={cs}
              defaultSecretName="azure-connection-string"
              onChange={(patch) => update(patch)}
            />
            <div style={{ fontSize: '11px', color: '#6b7280', marginTop: '2px' }}>
              Easiest option (also works with the Azurite emulator). Or set the account name + key below.
            </div>
          </div>
          <StyledInput
            label="Account name (optional, instead of connection string)"
            placeholder="mystorageaccount"
            value={(cs.account_name as string) || ''}
            onChange={(v) => update({ account_name: v })}
          />
          <div>
            <SecretInput
              label="Account key"
              placeholder="Azure storage account key"
              field="account_key"
              config={cs}
              defaultSecretName="azure-account-key"
              onChange={(patch) => update(patch)}
            />
          </div>
        </>
      )}

      {provider === 'gcs' && (
        <>
          <StyledInput
            label="Endpoint (optional, for a custom/private GCS endpoint)"
            placeholder="https://storage.googleapis.com/storage/v1/"
            value={(cs.endpoint as string) || ''}
            onChange={(v) => update({ endpoint: v })}
          />
          <div>
            <SecretInput
              label="Service account JSON"
              placeholder='{"type":"service_account", …}'
              field="credentials_json"
              config={cs}
              defaultSecretName="gcs-credentials-json"
              onChange={(patch) => update(patch)}
            />
            <div style={{ fontSize: '11px', color: '#6b7280', marginTop: '2px' }}>
              Paste the service-account key JSON. (For the local fake-gcs emulator, leave this
              blank and run the worker with <code>STORAGE_EMULATOR_HOST</code> set — an endpoint
              alone doesn't cover the GCS client's read path.)
            </div>
          </div>
        </>
      )}

      {role === 'consumer' ? (
        <>
          <StyledSelect
            label="Ingestion mode"
            value={mode}
            onChange={(v) => update({ mode: v })}
            options={[
              { value: 'poll', label: 'Poll (list the bucket on an interval)' },
              { value: 'event', label: 'Event-driven (queue / subscription)' },
            ]}
          />

          {mode === 'event' ? (
            <>
              {provider === 's3' && (
                <>
                  <StyledInput
                    label="SQS queue URL"
                    placeholder="https://sqs.us-east-1.amazonaws.com/123456789012/my-queue"
                    value={(cs.event_queue_url as string) || ''}
                    onChange={(v) => update({ event_queue_url: v })}
                  />
                  <StyledInput
                    label="SQS endpoint override (optional, e.g. LocalStack)"
                    placeholder="http://localstack:4566"
                    value={(cs.event_endpoint as string) || ''}
                    onChange={(v) => update({ event_endpoint: v })}
                  />
                  <div style={{ fontSize: '11px', color: '#6b7280' }}>
                    Subscribe the bucket's notifications to this SQS queue. Each object is ingested
                    as it arrives; the message is acked only after a successful publish. The
                    endpoint override is for the SQS service (distinct from the object-store
                    endpoint above); leave blank for real AWS SQS.
                  </div>
                </>
              )}
              {provider === 'azure' && (
                <>
                  <StyledInput
                    label="Storage Queue name"
                    placeholder="blob-events"
                    value={(cs.event_queue_name as string) || ''}
                    onChange={(v) => update({ event_queue_name: v })}
                  />
                  <StyledInput
                    label="Queue endpoint override (optional, e.g. Azurite)"
                    placeholder="http://azurite:10001/devstoreaccount1"
                    value={(cs.event_endpoint as string) || ''}
                    onChange={(v) => update({ event_endpoint: v })}
                  />
                  <div style={{ fontSize: '11px', color: '#6b7280' }}>
                    Route the container's Blob events through Event Grid into this Storage Queue.
                    The queue is long-polled and each message is deleted only after a successful
                    publish. It uses the same connection string / account credentials as the
                    container above; the endpoint override targets the Azurite emulator.
                  </div>
                </>
              )}
              {provider === 'gcs' && (
                <>
                  <StyledInput
                    label="Pub/Sub subscription"
                    placeholder="gcs-events-sub (or projects/…/subscriptions/…)"
                    value={(cs.event_subscription as string) || ''}
                    onChange={(v) => update({ event_subscription: v })}
                  />
                  <StyledInput
                    label="GCP project ID (required for a bare subscription name)"
                    placeholder="my-project"
                    value={(cs.event_project as string) || ''}
                    onChange={(v) => update({ event_project: v })}
                  />
                  <StyledInput
                    label="Pub/Sub endpoint override (optional, e.g. emulator)"
                    placeholder="localhost:8085"
                    value={(cs.event_endpoint as string) || ''}
                    onChange={(v) => update({ event_endpoint: v })}
                  />
                  <div style={{ fontSize: '11px', color: '#6b7280' }}>
                    Create a bucket notification to a Pub/Sub topic and a pull subscription on it.
                    Messages are pulled and acked only after a successful publish (OBJECT_FINALIZE
                    events are ingested). Uses the service-account JSON above; the endpoint
                    override targets the Pub/Sub emulator.
                  </div>
                </>
              )}
            </>
          ) : (
            <StyledInput
              label="File pattern (optional)"
              placeholder="*.csv"
              value={(cs.file_pattern as string) || ''}
              onChange={(v) => update({ file_pattern: v })}
            />
          )}

          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
            {mode !== 'event' && (
              <StyledInput
                label="Poll interval (s, 0 = once)"
                type="number"
                placeholder="60"
                value={String((cs.poll_interval_seconds as number) ?? DEFAULT_POLL_INTERVAL_SECONDS)}
                onChange={(v) => update({ poll_interval_seconds: parseInt(v) || 0 })}
              />
            )}
            <StyledSelect
              label="After action"
              value={afterAction}
              onChange={(v) => update({ after_action: v })}
              options={[
                { value: 'none', label: 'Leave in place' },
                { value: 'delete', label: 'Delete' },
                { value: 'move', label: 'Move' },
              ]}
            />
          </div>
          {afterAction === 'move' && (
            <StyledInput
              label="Move to prefix"
              placeholder="processed/"
              value={(cs.move_prefix as string) || ''}
              onChange={(v) => update({ move_prefix: v })}
            />
          )}
        </>
      ) : (
        <>
          <StyledInput
            label="Key template"
            placeholder="orders/{{.id}}_{{.timestamp}}.json"
            value={(cs.key_template as string) || ''}
            onChange={(v) => update({ key_template: v })}
          />
          {(() => {
            const sse = (cs.sse as Record<string, unknown>) || {}
            const sseMode = (sse.mode as string) || 'none'
            const updateSSE = (patch: Record<string, unknown>) =>
              update({ sse: { ...sse, ...patch } })
            return (
              <>
                <StyledSelect
                  label="Server-side encryption"
                  value={sseMode}
                  onChange={(v) => updateSSE({ mode: v })}
                  options={[
                    { value: 'none', label: 'Bucket default' },
                    { value: 'sse-s3', label: 'SSE-S3 (AES-256, S3 only)' },
                    { value: 'sse-kms', label: 'KMS / CMEK key' },
                  ]}
                />
                {sseMode === 'sse-kms' && (
                  <StyledInput
                    label={provider === 's3' ? 'KMS key ID/ARN' : provider === 'azure' ? 'Encryption scope name' : 'Cloud KMS key name'}
                    placeholder={provider === 's3' ? 'arn:aws:kms:…' : provider === 'azure' ? 'my-encryption-scope' : 'projects/…/cryptoKeys/…'}
                    value={(sse.kms_key_id as string) || ''}
                    onChange={(v) => updateSSE({ kms_key_id: v })}
                  />
                )}
              </>
            )
          })()}
        </>
      )}

      <div style={{ fontSize: '11px', color: '#6b7280' }}>
        {role === 'consumer'
          ? 'The bucket is polled under the prefix and each new object is ingested.'
          : 'Each message is written as an object. The key template can reference payload fields plus the timestamp and uuid built-ins; it defaults to the message id.'}{' '}
        Credentials are stored encrypted in the secrets vault on deploy.
      </div>
      <TestConnectionButton config={config} role={role} />
    </div>
  )
}
