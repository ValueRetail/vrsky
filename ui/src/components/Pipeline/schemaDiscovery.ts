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

// postJSON sends a discovery request through the management API, which
// authenticates it and forwards it to the connector that owns that source type.
//
// These used to be bare fetches at http://localhost:<worker-port> — thirteen of
// them. That worked in compose and nowhere else, so "Discover fields" was dead
// in every deployment; and the bodies carry credentials (Kafka's password and
// client key, RabbitMQ's URL, the API source's auth_value, SFTP's and SAP's
// whole config) which were being posted to a port with no authentication at
// all. Going through apiClient means the session cookie, bearer token and
// workspace header are attached the same way as every other API call.
//
// The server sets tenant_id from the session and ignores any sent here, so a
// browser cannot ask a connector to resolve another workspace's secrets.
async function postJSON(url: string, body: unknown): Promise<Record<string, unknown>> {
  try {
    const resp = await apiClient.post(url, body)
    return (resp.data ?? {}) as Record<string, unknown>
  } catch (e) {
    // The connectors answer logical failures with 200 + {ok:false}, which the
    // callers below already handle. Reaching here means transport, auth, or a
    // connector that is not running.
    throw new Error(e instanceof Error ? e.message : 'Could not reach the connector')
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
      const data = await postJSON(`/api/v1/schema-discovery/database`, {
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
      const data = await postJSON(`/api/v1/schema-discovery/file`, { path: fc.path })
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
      const data = await postJSON(`/api/v1/schema-discovery/api`, {
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
      const data = await postJSON(`/api/v1/schema-discovery/salesforce`, {
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
      const data = await postJSON(`/api/v1/schema-discovery/kafka`, {
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
      const data = await postJSON(`/api/v1/schema-discovery/rabbitmq`, {
        url: rc.url, username: rc.username, password: rc.password, queue: rc.queue,
      })
      if (!data.ok) throw new Error((data.error as string) || 'No messages on the queue to sample yet')
      return inferSchema(data.data)
    }

    case 'sap_s4hana': {
      const sap = (consumerConfig.sap_s4hana as Record<string, unknown>) || {}
      if (!sap.api_base_url && !sap.host) throw new Error('Set the SAP host or API base URL first')
      if (!sap.entity_set) throw new Error('Set the entity set first')
      const data = await postJSON(`/api/v1/schema-discovery/sap_s4hana`, { ...sap })
      if (!data.ok) throw new Error((data.error as string) || 'Failed to fetch a sample from SAP')
      return inferSchema(data.data)
    }

    case 'sftp': {
      const sftp = (consumerConfig.sftp as Record<string, unknown>) || {}
      if (!sftp.host) throw new Error('Set the SFTP host first')
      const data = await postJSON(`/api/v1/schema-discovery/sftp`, { ...sftp })
      if (!data.ok) throw new Error((data.error as string) || 'No files in the remote directory to sample yet')
      return inferSchema(data.data)
    }

    case 'cloud_storage': {
      const cs = (consumerConfig.cloud_storage as Record<string, unknown>) || {}
      if (!cs.bucket) throw new Error('Set the cloud storage bucket first')
      const data = await postJSON(`/api/v1/schema-discovery/cloud_storage`, { ...cs })
      if (!data.ok) throw new Error((data.error as string) || 'No objects under the prefix to sample yet')
      return inferSchema(data.data)
    }

    case 'sitoo': {
      const sc = (consumerConfig.sitoo as Record<string, unknown>) || {}
      if (!sc.api_id || !sc.account_id || !sc.site_id) throw new Error('Set the Sitoo API ID, account ID and site ID first')
      const data = await postJSON(`/api/v1/schema-discovery/sitoo`, { ...sc })
      if (!data.ok) throw new Error((data.error as string) || 'No Sitoo data to sample yet')
      return inferSchema(data.data)
    }

    case 'business_central': {
      const bc = (consumerConfig.business_central as Record<string, unknown>) || {}
      if (!bc.client_id || (!bc.client_secret && !bc.client_secret_secret_id)) throw new Error('Set the Business Central client ID and secret first')
      const data = await postJSON(`/api/v1/schema-discovery/business_central`, { ...bc })
      if (!data.ok) throw new Error((data.error as string) || 'No Business Central data to sample yet')
      return inferSchema(data.data)
    }

    case 'visma': {
      const vc = (consumerConfig.visma as Record<string, unknown>) || {}
      if (!vc.client_id || !vc.base_url || !vc.resource) throw new Error('Set the Visma client ID, base URL and resource first')
      const data = await postJSON(`/api/v1/schema-discovery/visma`, { ...vc })
      if (!data.ok) throw new Error((data.error as string) || 'No Visma data to sample yet')
      return inferSchema(data.data)
    }

    case 'brightpearl': {
      const bp = (consumerConfig.brightpearl as Record<string, unknown>) || {}
      if (!bp.app_ref || (!bp.staff_token && !bp.staff_token_secret_id) || !bp.resource) throw new Error('Set the Brightpearl app ref, staff token and resource first')
      const data = await postJSON(`/api/v1/schema-discovery/brightpearl`, { ...bp })
      if (!data.ok) throw new Error((data.error as string) || 'No Brightpearl data to sample yet')
      return inferSchema(data.data)
    }

    default: {
      // Front Systems (push-only) + http/webhook: the only sample is a deployed
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
