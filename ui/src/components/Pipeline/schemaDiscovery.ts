// discoverSchema (#81): fetch a source's field schema for the mapping UI.
// DB uses the authoritative information_schema endpoint; the other sources reuse
// the existing /sample-data/ endpoints and infer types client-side.
import apiClient from '../../services/api'
import { inferSchema, fieldsFromColumns, sqlTypeToBadge, sfTypeToBadge, type SchemaField } from './schema'

export type { SchemaField } from './schema'

interface DBColumn {
  name: string
  type?: string
  nullable?: boolean
}

// postJSON POSTs to a worker aux endpoint and parses JSON, surfacing a readable
// error for non-2xx / non-JSON responses (e.g. a proxy HTML error page) instead
// of an opaque SyntaxError.
async function postJSON(url: string, body: unknown): Promise<Record<string, unknown>> {
  let resp: Response
  try {
    resp = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
  } catch {
    throw new Error('Could not reach the connector — is the worker running?')
  }
  if (!resp.ok) {
    throw new Error(`Connector responded ${resp.status}`)
  }
  try {
    return await resp.json()
  } catch {
    throw new Error('Connector returned a non-JSON response')
  }
}

// discoverSchema returns the source field tree for the upstream consumer feeding
// a converter/filter node. Throws with a user-facing message when the source
// isn't configured or the worker can't be reached.
export async function discoverSchema(
  consumerType: string | undefined,
  consumerConfig: Record<string, unknown> | undefined,
  opts?: { deployedConnectionId?: string; tenantId?: string },
): Promise<SchemaField[]> {
  if (!consumerType || !consumerConfig) {
    throw new Error('No upstream source is connected to this node')
  }

  switch (consumerType) {
    case 'database': {
      const dc = (consumerConfig.database as Record<string, unknown>) || {}
      if (!dc.host || !dc.table) throw new Error('Set the database host and table on the input first')
      const data = await postJSON('http://localhost:9300/schema/', {
        host: dc.host, port: dc.port || 5432, user: dc.user, password: dc.password,
        database: dc.database, sslmode: dc.sslmode, table: dc.table,
      })
      if (!data.ok) throw new Error((data.error as string) || 'Schema query failed')
      const cols = (data.fields as DBColumn[] | undefined) || []
      return fieldsFromColumns(cols.map((f) => ({ name: f.name, type: sqlTypeToBadge(f.type || ''), nullable: f.nullable })))
    }

    case 'file': {
      const fc = (consumerConfig.file as Record<string, unknown>) || {}
      if (!fc.path) throw new Error('Set a watch directory on the file input first')
      const data = await postJSON('http://localhost:9200/sample-data/', { path: fc.path })
      if (!data.ok) throw new Error((data.error as string) || 'No files in the watch directory')
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
      const data = await postJSON('http://localhost:9800/sample-data/', {
        base_url: api.base_url, path: (ep.path as string) || '/', params: (ep.params as string) || '',
        auth_type: (ep.auth_type as string) || 'none', auth_value: (ep.auth_value as string) || '',
      })
      if (!data.ok) throw new Error((data.error as string) || 'Sample request failed')
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

    case 'salesforce': {
      const sf = (consumerConfig.salesforce as Record<string, unknown>) || {}
      if (!sf.instance_url || !sf.oauth_grant_id) throw new Error('Set the Salesforce instance URL and connect an account first')
      if (!sf.soql) throw new Error('Enter a SOQL query (its FROM clause names the object to describe)')
      if (!opts?.tenantId) throw new Error('No active tenant')
      const data = await postJSON('http://localhost:9250/schema/', {
        tenant_id: opts.tenantId,
        instance_url: sf.instance_url, oauth_grant_id: sf.oauth_grant_id,
        api_version: sf.api_version, soql: sf.soql,
      })
      if (!data.ok) throw new Error((data.error as string) || 'Salesforce describe failed')
      const cols = (data.fields as DBColumn[] | undefined) || []
      return fieldsFromColumns(cols.map((f) => ({ name: f.name, type: sfTypeToBadge(f.type || ''), nullable: f.nullable })))
    }

    case 'kafka': {
      const kc = (consumerConfig.kafka as Record<string, unknown>) || {}
      if (!kc.brokers || !kc.topic) throw new Error('Set the Kafka brokers and topic on the input first')
      const data = await postJSON('http://localhost:9220/sample-data/', {
        brokers: kc.brokers, topic: kc.topic, consumer_group: kc.consumer_group,
        auth_type: kc.auth_type, username: kc.username, password: kc.password,
        ca_cert: kc.ca_cert, client_cert: kc.client_cert, client_key: kc.client_key,
      })
      if (!data.ok) throw new Error((data.error as string) || 'No messages on the topic to sample yet')
      return inferSchema(data.data)
    }

    case 'rabbitmq': {
      const rc = (consumerConfig.rabbitmq as Record<string, unknown>) || {}
      if (!rc.url || !rc.queue) throw new Error('Set the RabbitMQ URL and queue on the input first')
      const data = await postJSON('http://localhost:9230/sample-data/', {
        url: rc.url, username: rc.username, password: rc.password, queue: rc.queue,
      })
      if (!data.ok) throw new Error((data.error as string) || 'No messages on the queue to sample yet')
      return inferSchema(data.data)
    }

    case 'sap_s4hana': {
      const sap = (consumerConfig.sap_s4hana as Record<string, unknown>) || {}
      if (!sap.api_base_url && !sap.host) throw new Error('Set the SAP host or API base URL first')
      if (!sap.entity_set) throw new Error('Set the entity set first')
      const data = await postJSON('http://localhost:9290/sample-data/', { ...sap, tenant_id: opts?.tenantId })
      if (!data.ok) throw new Error((data.error as string) || 'Failed to fetch a sample from SAP')
      return inferSchema(data.data)
    }

    case 'sftp': {
      const sftp = (consumerConfig.sftp as Record<string, unknown>) || {}
      if (!sftp.host) throw new Error('Set the SFTP host first')
      const data = await postJSON('http://localhost:9210/sample-data/', { ...sftp, tenant_id: opts?.tenantId })
      if (!data.ok) throw new Error((data.error as string) || 'No files in the remote directory to sample yet')
      return inferSchema(data.data)
    }

    case 'cloud_storage': {
      const cs = (consumerConfig.cloud_storage as Record<string, unknown>) || {}
      if (!cs.bucket) throw new Error('Set the cloud storage bucket first')
      const data = await postJSON('http://localhost:9240/sample-data/', { ...cs, tenant_id: opts?.tenantId })
      if (!data.ok) throw new Error((data.error as string) || 'No objects under the prefix to sample yet')
      return inferSchema(data.data)
    }

    default: {
      // http / webhook (and the retail/ERP connectors without a test endpoint
      // yet): the only sample is a deployed connection's last payload.
      if (!opts?.deployedConnectionId) {
        throw new Error('Deploy the pipeline and send data once, then discover the schema')
      }
      const resp = await apiClient.get(`/api/v1/connections/${opts.deployedConnectionId}/sample-data`)
      if (!resp.data?.ok) throw new Error(resp.data?.error || 'No sample payload yet')
      return inferSchema(resp.data.data)
    }
  }
}
