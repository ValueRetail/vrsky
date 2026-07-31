/**
 * Pipeline templates for the first-login onboarding wizard (Phase 4B / #93).
 *
 * Each template is a fully pre-filled connection (consumer + producer nodes +
 * the edge between them) plus a small list of `fields` the user must supply —
 * almost always just credentials or one destination value. The wizard clones
 * the template, lets the user fill those fields, then deploys via the same
 * createConnection → materializeSecrets → start path the visual builder uses.
 *
 * Templates use only connectors that exist today. "Webhook → Slack" is an HTTP
 * producer pointed at a Slack incoming-webhook URL (a Slack webhook is just an
 * HTTP POST), so it needs no dedicated Slack connector.
 */

export type TemplateNode = {
  id: string
  type: 'consumer' | 'producer'
  // Pre-filled connector config, e.g. { type: 'http', http: { url, method } }.
  config: Record<string, unknown>
}

export type TemplateField = {
  /** Node the field belongs to. */
  nodeId: string
  /** Dot-path to the object holding `key` within that node's config. '' = root. */
  objectPath: string
  /** Key within that object, e.g. 'url' or 'connection_string'. */
  key: string
  label: string
  /** Inline help shown under the field. */
  help: string
  /** Optional "learn more" link. */
  link?: { text: string; url: string }
  /** Render a masked SecretInput; key must be in SECRET_FIELDS. */
  secret?: boolean
  placeholder?: string
}

export type PipelineTemplate = {
  id: string
  name: string
  /** One-line plain-English description for the gallery card. */
  summary: string
  icon: string
  sourceLabel: string
  destLabel: string
  nodes: TemplateNode[]
  edges: { source: string; target: string }[]
  fields: TemplateField[]
  /** True when the source is an inbound webhook — the wizard then shows the
   *  public URL and a "send a sample event" button so the user sees it work. */
  webhookSource?: boolean
  /** Body POSTed by the "send sample event" button (webhook templates). */
  samplePayload?: unknown
}

export const TEMPLATES: PipelineTemplate[] = [
  {
    id: 'webhook-to-slack',
    name: 'Webhook → Slack',
    summary: 'Receive webhook events and post them to a Slack channel.',
    icon: '💬',
    sourceLabel: 'Webhook',
    destLabel: 'Slack',
    nodes: [
      { id: 'src', type: 'consumer', config: { type: 'http' } },
      {
        id: 'dst',
        type: 'producer',
        config: {
          type: 'http',
          http: { url: '', method: 'POST', headers: { 'Content-Type': 'application/json' } },
        },
      },
    ],
    edges: [{ source: 'src', target: 'dst' }],
    fields: [
      {
        nodeId: 'dst',
        objectPath: 'http',
        key: 'url',
        label: 'Slack incoming webhook URL',
        help: 'Create one in Slack: Apps → Incoming Webhooks → Add to a channel. It looks like https://hooks.slack.com/services/...',
        link: { text: 'Slack: create an incoming webhook', url: 'https://api.slack.com/messaging/webhooks' },
        placeholder: 'https://hooks.slack.com/services/T000/B000/XXXX',
      },
    ],
    webhookSource: true,
    samplePayload: { text: 'Hello from VRSky 👋 — your pipeline is live!' },
  },
  {
    id: 'webhook-to-http',
    name: 'Webhook → HTTP endpoint',
    summary: 'Forward incoming webhooks to any HTTP API.',
    icon: '🔗',
    sourceLabel: 'Webhook',
    destLabel: 'HTTP',
    nodes: [
      { id: 'src', type: 'consumer', config: { type: 'http' } },
      { id: 'dst', type: 'producer', config: { type: 'http', http: { url: '', method: 'POST' } } },
    ],
    edges: [{ source: 'src', target: 'dst' }],
    fields: [
      {
        nodeId: 'dst',
        objectPath: 'http',
        key: 'url',
        label: 'Destination URL',
        help: 'The HTTP endpoint each event is POSTed to.',
        placeholder: 'https://api.example.com/ingest',
      },
    ],
    webhookSource: true,
    samplePayload: { hello: 'world', from: 'vrsky' },
  },
  {
    id: 'webhook-to-file',
    name: 'Webhook → File',
    summary: 'Capture incoming webhooks to files on disk.',
    icon: '📁',
    sourceLabel: 'Webhook',
    destLabel: 'File',
    nodes: [
      { id: 'src', type: 'consumer', config: { type: 'http' } },
      { id: 'dst', type: 'producer', config: { type: 'file', file: { path: '/data/output' } } },
    ],
    edges: [{ source: 'src', target: 'dst' }],
    fields: [
      {
        nodeId: 'dst',
        objectPath: 'file',
        key: 'path',
        label: 'Output folder',
        help: 'Folder the producer writes each event to. Leave the default unless you mounted another path.',
        placeholder: '/data/output',
      },
    ],
    webhookSource: true,
    samplePayload: { event: 'sample', ts: 'now' },
  },
  {
    id: 'csv-to-database',
    name: 'CSV → Database',
    summary: 'Load uploaded CSV rows into a database table.',
    icon: '🗄️',
    sourceLabel: 'CSV file',
    destLabel: 'Database',
    nodes: [
      { id: 'src', type: 'consumer', config: { type: 'file', file: { format: 'csv' } } },
      {
        id: 'dst',
        type: 'producer',
        config: { type: 'database', database: { connection_string: '', table: '', operation: 'insert' } },
      },
    ],
    edges: [{ source: 'src', target: 'dst' }],
    fields: [
      {
        nodeId: 'dst',
        objectPath: 'database',
        key: 'connection_string',
        label: 'Database connection string',
        help: 'Postgres URL of the target database. Stored encrypted — never saved in plain text.',
        secret: true,
        placeholder: 'postgres://user:pass@host:5432/dbname',
      },
      {
        nodeId: 'dst',
        objectPath: 'database',
        key: 'table',
        label: 'Target table',
        help: 'The table each CSV row is inserted into.',
        placeholder: 'public.imported_rows',
      },
    ],
  },
  {
    id: 'api-to-file',
    name: 'API poll → File',
    summary: 'Poll a REST API on a schedule and write each response to disk.',
    icon: '⏱️',
    sourceLabel: 'REST API',
    destLabel: 'File',
    nodes: [
      { id: 'src', type: 'consumer', config: { type: 'api', api: { url: '', method: 'GET', interval: '60s' } } },
      { id: 'dst', type: 'producer', config: { type: 'file', file: { path: '/data/output' } } },
    ],
    edges: [{ source: 'src', target: 'dst' }],
    fields: [
      {
        nodeId: 'src',
        objectPath: 'api',
        key: 'url',
        label: 'API URL',
        help: 'A REST endpoint returning JSON. It is polled on the interval below.',
        placeholder: 'https://api.example.com/v1/items',
      },
      {
        nodeId: 'src',
        objectPath: 'api',
        key: 'interval',
        label: 'Poll interval',
        help: 'How often to call the API, e.g. 30s, 5m, 1h.',
        placeholder: '60s',
      },
      {
        nodeId: 'dst',
        objectPath: 'file',
        key: 'path',
        label: 'Output folder',
        help: 'Folder each response is written to.',
        placeholder: '/data/output',
      },
    ],
  },
  // --- Retail: omnichannel sync (POS ⇄ ERP) --------------------------------
  // The flagship demo scenarios — connect a store POS to the ERP so sales and
  // stock stay in sync. Both directions of the same two systems.
  {
    id: 'sitoo-to-business-central',
    name: 'POS sales → ERP (Sitoo → Business Central)',
    summary: 'Sync completed POS sales from Sitoo into Dynamics 365 Business Central as sales orders.',
    icon: '🛒',
    sourceLabel: 'Sitoo (POS)',
    destLabel: 'Business Central (ERP)',
    nodes: [
      {
        id: 'src',
        type: 'consumer',
        config: { type: 'sitoo', sitoo: { resource: 'transactions', poll_interval_seconds: 300 } },
      },
      {
        id: 'dst',
        type: 'producer',
        config: { type: 'business_central', business_central: { entity: 'salesOrders', method: 'POST', environment: 'Production' } },
      },
    ],
    edges: [{ source: 'src', target: 'dst' }],
    fields: [
      { nodeId: 'src', objectPath: 'sitoo', key: 'account_id', label: 'Sitoo account ID', help: 'Your Sitoo account number.', placeholder: '12345' },
      { nodeId: 'src', objectPath: 'sitoo', key: 'site_id', label: 'Sitoo site ID', help: 'The Sitoo site to read transactions from.', placeholder: '1' },
      { nodeId: 'src', objectPath: 'sitoo', key: 'api_id', label: 'Sitoo API ID', help: 'From Sitoo Backoffice → Settings → Sitoo REST API.', placeholder: 'your-api-id' },
      { nodeId: 'src', objectPath: 'sitoo', key: 'api_password', label: 'Sitoo API password', help: 'Stored encrypted — never saved in plain text.', secret: true, placeholder: 'API password' },
      { nodeId: 'dst', objectPath: 'business_central', key: 'aad_tenant_id', label: 'Business Central Entra tenant ID', help: 'Your Microsoft Entra (Azure AD) tenant GUID or domain.', link: { text: 'BC: OAuth setup', url: 'https://learn.microsoft.com/dynamics365/business-central/dev-itpro/webservices/authenticate-web-services-using-oauth' }, placeholder: '<tenant-guid>' },
      { nodeId: 'dst', objectPath: 'business_central', key: 'company_id', label: 'BC company ID (GUID)', help: 'The Business Central company the orders are created in.', placeholder: '<company-guid>' },
      { nodeId: 'dst', objectPath: 'business_central', key: 'client_id', label: 'BC app (client) ID', help: 'The Entra app registration id with Business Central API.ReadWrite.All.', placeholder: '<app-id>' },
      { nodeId: 'dst', objectPath: 'business_central', key: 'client_secret', label: 'BC client secret', help: 'The Entra app client secret. Stored encrypted.', secret: true, placeholder: 'client secret' },
    ],
  },
  {
    id: 'business-central-to-sitoo',
    name: 'ERP inventory → POS (Business Central → Sitoo)',
    summary: 'Push item stock levels from Dynamics 365 Business Central out to Sitoo POS to prevent overselling.',
    icon: '📦',
    sourceLabel: 'Business Central (ERP)',
    destLabel: 'Sitoo (POS)',
    nodes: [
      {
        id: 'src',
        type: 'consumer',
        config: { type: 'business_central', business_central: { entity: 'items', poll_interval_seconds: 300, environment: 'Production' } },
      },
      {
        id: 'dst',
        type: 'producer',
        config: { type: 'sitoo', sitoo: { resource: 'warehouseitems', method: 'POST' } },
      },
    ],
    edges: [{ source: 'src', target: 'dst' }],
    fields: [
      { nodeId: 'src', objectPath: 'business_central', key: 'aad_tenant_id', label: 'Business Central Entra tenant ID', help: 'Your Microsoft Entra (Azure AD) tenant GUID or domain.', placeholder: '<tenant-guid>' },
      { nodeId: 'src', objectPath: 'business_central', key: 'company_id', label: 'BC company ID (GUID)', help: 'The Business Central company to read items from.', placeholder: '<company-guid>' },
      { nodeId: 'src', objectPath: 'business_central', key: 'client_id', label: 'BC app (client) ID', help: 'The Entra app registration id.', placeholder: '<app-id>' },
      { nodeId: 'src', objectPath: 'business_central', key: 'client_secret', label: 'BC client secret', help: 'Stored encrypted.', secret: true, placeholder: 'client secret' },
      { nodeId: 'dst', objectPath: 'sitoo', key: 'account_id', label: 'Sitoo account ID', help: 'Your Sitoo account number.', placeholder: '12345' },
      { nodeId: 'dst', objectPath: 'sitoo', key: 'site_id', label: 'Sitoo site ID', help: 'The Sitoo site whose stock is updated.', placeholder: '1' },
      { nodeId: 'dst', objectPath: 'sitoo', key: 'api_id', label: 'Sitoo API ID', help: 'From Sitoo Backoffice → Settings → Sitoo REST API.', placeholder: 'your-api-id' },
      { nodeId: 'dst', objectPath: 'sitoo', key: 'api_password', label: 'Sitoo API password', help: 'Stored encrypted.', secret: true, placeholder: 'API password' },
    ],
  },
]

export function templateById(id: string): PipelineTemplate | undefined {
  return TEMPLATES.find((t) => t.id === id)
}
