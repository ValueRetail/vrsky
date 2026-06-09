// discoverSchema (#81): fetch a source's field schema for the mapping UI.
// DB uses the authoritative information_schema endpoint; the other sources reuse
// the existing /sample-data/ endpoints and infer types client-side.
import apiClient from '../../services/api'
import { inferSchema, fieldsFromColumns, sqlTypeToBadge, type SchemaField } from './schema'

export type { SchemaField } from './schema'

interface DBColumn {
  name: string
  type?: string
  nullable?: boolean
}

// discoverSchema returns the source field tree for the upstream consumer feeding
// a converter/filter node. Throws with a user-facing message when the source
// isn't configured or the worker can't be reached.
export async function discoverSchema(
  consumerType: string | undefined,
  consumerConfig: Record<string, unknown> | undefined,
  opts?: { deployedConnectionId?: string },
): Promise<SchemaField[]> {
  if (!consumerType || !consumerConfig) {
    throw new Error('No upstream source is connected to this node')
  }

  switch (consumerType) {
    case 'database': {
      const dc = (consumerConfig.database as Record<string, unknown>) || {}
      if (!dc.host || !dc.table) throw new Error('Set the database host and table on the input first')
      const resp = await fetch('http://localhost:9300/schema/', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          host: dc.host, port: dc.port || 5432, user: dc.user, password: dc.password,
          database: dc.database, sslmode: dc.sslmode, table: dc.table,
        }),
      })
      const data = await resp.json()
      if (!data.ok) throw new Error(data.error || 'Schema query failed')
      const cols = (data.fields as DBColumn[] | undefined) || []
      return fieldsFromColumns(cols.map((f) => ({ name: f.name, type: sqlTypeToBadge(f.type || ''), nullable: f.nullable })))
    }

    case 'file': {
      const fc = (consumerConfig.file as Record<string, unknown>) || {}
      if (!fc.path) throw new Error('Set a watch directory on the file input first')
      const resp = await fetch('http://localhost:9200/sample-data/', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path: fc.path }),
      })
      const data = await resp.json()
      if (!data.ok) throw new Error(data.error || 'No files in the watch directory')
      const columns = data.columns as string[] | undefined
      if (Array.isArray(columns) && columns.length > 0) {
        return fieldsFromColumns(columns.map((c) => ({ name: c, type: 'string' as const })))
      }
      return inferSchema(data.data)
    }

    case 'api': {
      const api = (consumerConfig.api as { base_url?: string; endpoints?: Array<Record<string, unknown>> }) || {}
      const ep = api.endpoints?.[0]
      if (!api.base_url || !ep) throw new Error('Set the API base URL and an endpoint first')
      const resp = await fetch('http://localhost:9800/sample-data/', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          base_url: api.base_url, path: (ep.path as string) || '/', params: (ep.params as string) || '',
          auth_type: (ep.auth_type as string) || 'none', auth_value: (ep.auth_value as string) || '',
        }),
      })
      const data = await resp.json()
      if (!data.ok) throw new Error(data.error || 'Sample request failed')
      return inferSchema(data.data)
    }

    case 'tenant': {
      const t = (consumerConfig.tenant as { source_tenant_id?: string; source_connection_id?: string }) || {}
      if (!t.source_tenant_id) throw new Error('Configure the tenant data source first')
      const params = new URLSearchParams({ source_tenant_id: t.source_tenant_id })
      if (t.source_connection_id) params.set('source_connection_id', t.source_connection_id)
      const resp = await apiClient.get(`/api/v1/sample-data/source?${params.toString()}`)
      if (!resp.data?.ok) throw new Error(resp.data?.error || 'Sample request failed')
      return inferSchema(resp.data.data)
    }

    default: {
      // http / webhook (and anything else): the only sample is a deployed
      // connection's last payload.
      if (!opts?.deployedConnectionId) {
        throw new Error('Deploy the pipeline and send data once, then discover the schema')
      }
      const resp = await apiClient.get(`/api/v1/connections/${opts.deployedConnectionId}/sample-data`)
      if (!resp.data?.ok) throw new Error(resp.data?.error || 'No sample payload yet')
      return inferSchema(resp.data.data)
    }
  }
}
