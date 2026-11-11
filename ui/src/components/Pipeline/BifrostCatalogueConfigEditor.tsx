import SecretInput from './SecretInput'
import { StyledInput, StyledSelect } from './StyledFields'

// BifrostCatalogueConfigEditor (#815): the destination settings for Bifrost's
// catalogue-api. Stores config.bifrost_catalogue = {base_url, token, tenant_id,
// mode, batch_size, allow_shrink_reason}. token is minted into the secrets
// vault on deploy (SECRET_FIELDS) and replaced by token_secret_id; the
// connector refuses it inline.

const hintStyle: React.CSSProperties = { fontSize: '11px', color: '#6b7280', marginBottom: '12px' }

// batchSize keeps only a positive whole number. Anything else (empty, NaN, a
// fraction, a negative) leaves batch_size unset so the connector's default
// applies: NaN would be saved as null and 1.5 cannot be read into the
// connector's integer setting.
function batchSize(v: string): number | undefined {
  const n = Number(v.trim())
  return v.trim() !== '' && Number.isInteger(n) && n > 0 ? n : undefined
}

export default function BifrostCatalogueConfigEditor({
  config,
  setConfig,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
}) {
  const c = (config.bifrost_catalogue as Record<string, unknown>) || {}
  const update = (patch: Record<string, unknown>) =>
    setConfig({ ...config, bifrost_catalogue: { ...c, ...patch } })
  const mode = (c.mode as string) || 'delta'

  return (
    <div className="space-y-3">
      <StyledInput
        label="Base URL"
        placeholder="https://catalogue.example.com"
        value={(c.base_url as string) || ''}
        onChange={(v) => update({ base_url: v })}
      />
      <SecretInput
        label="Tenant token"
        placeholder="Bifrost bearer token with catalogue.import"
        field="token"
        config={c}
        defaultSecretName="bifrost-catalogue-token"
        onChange={(patch) => update(patch)}
      />
      <StyledInput
        label="Bifrost tenant ID (optional)"
        placeholder="nordic"
        value={(c.tenant_id as string) || ''}
        onChange={(v) => update({ tenant_id: v })}
      />
      <StyledSelect
        label="Import"
        value={mode}
        onChange={(v) => update({ mode: v })}
        options={[
          { value: 'delta', label: 'Delta: upsert the items' },
          { value: 'full', label: 'Full: the items replace the catalogue' },
        ]}
      />
      {mode === 'full' && (
        <div style={hintStyle}>
          Each message must hold the whole catalogue as one list. A source that sends one message per page or per
          article would replace the catalogue with each one.
        </div>
      )}
      <StyledInput
        label="Batch size"
        type="number"
        placeholder="1000"
        value={c.batch_size ? String(c.batch_size) : ''}
        onChange={(v) => update({ batch_size: batchSize(v) })}
      />
      <div style={hintStyle}>
        One message is one import: begin, the items in batches, commit. A failure abandons the import, so a retry never
        commits a half-filled one. Bifrost&apos;s shrink guard and validation refusals are not retried.
      </div>
    </div>
  )
}
