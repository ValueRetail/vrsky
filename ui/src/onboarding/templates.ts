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
 *
 * The two retail templates (Sitoo ⇄ Business Central) put a data-converter
 * between the systems, because a Sitoo receipt is not a BC sales invoice and a
 * BC item is not a Sitoo warehouse item. Their mappings are written out field
 * by field in plans/issue-435-sitoo-bc-mapping.md and tested in Go against the
 * real converter with the same config (./testdata/, kept equal to the configs
 * below by templates.test.ts). They are marked beta until a live run against
 * the BC trial and a Sitoo sandbox.
 */

export type TemplateNode = {
  id: string
  type: 'consumer' | 'converter' | 'producer'
  // Pre-filled config. Sources and destinations carry a connector type and its
  // settings block, e.g. { type: 'http', http: { url, method } }; a converter
  // carries its settings flat, e.g. { input_format: 'csv' }. Every key must be
  // one the connector reads: templates.test.ts checks them against
  // CONNECTOR_KEYS (types/nodeConfig.ts).
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
  /** Store the value as a number (ports, intervals): the connector reads an int. */
  numeric?: boolean
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

// Credential fields shared by both retail templates, for the node that talks
// to Sitoo or to Business Central.
function sitooFields(nodeId: string): TemplateField[] {
  return [
    { nodeId, objectPath: 'sitoo', key: 'account_id', label: 'Sitoo account number', help: 'The number in your Sitoo API address (…/accounts/<number>/…).', numeric: true, placeholder: '12345' },
    { nodeId, objectPath: 'sitoo', key: 'site_id', label: 'Sitoo site ID', help: 'The Sitoo site (store or webshop) to use.', numeric: true, placeholder: '1' },
    { nodeId, objectPath: 'sitoo', key: 'api_id', label: 'Sitoo API ID', help: 'From Sitoo Backoffice → Settings → Sitoo REST API.', placeholder: 'your-api-id' },
    { nodeId, objectPath: 'sitoo', key: 'api_password', label: 'Sitoo API password', help: 'Stored encrypted — never saved in plain text.', secret: true, placeholder: 'API password' },
  ]
}

function businessCentralFields(nodeId: string): TemplateField[] {
  return [
    {
      nodeId, objectPath: 'business_central', key: 'aad_tenant_id', label: 'Microsoft Entra tenant ID',
      help: 'The tenant GUID or domain your Business Central runs in.',
      link: { text: 'BC: OAuth setup', url: 'https://learn.microsoft.com/dynamics365/business-central/dev-itpro/webservices/authenticate-web-services-using-oauth' },
      placeholder: '<tenant-guid>',
    },
    { nodeId, objectPath: 'business_central', key: 'environment', label: 'BC environment', help: 'The environment name in the Business Central admin center, e.g. Production or Sandbox.', placeholder: 'Production' },
    { nodeId, objectPath: 'business_central', key: 'company_id', label: 'BC company ID (GUID)', help: 'The id of the company, from GET …/api/v2.0/companies.', placeholder: '<company-guid>' },
    { nodeId, objectPath: 'business_central', key: 'client_id', label: 'BC app (client) ID', help: 'The Entra app registration with Business Central API.ReadWrite.All, registered in BC under Microsoft Entra Applications.', placeholder: '<app-id>' },
    { nodeId, objectPath: 'business_central', key: 'client_secret', label: 'BC client secret', help: 'Stored encrypted — never saved in plain text.', secret: true, placeholder: 'client secret' },
  ]
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
    summary: 'Load the rows of CSV files dropped in a folder into a database table.',
    icon: '🗄️',
    sourceLabel: 'CSV file',
    destLabel: 'Database',
    nodes: [
      // The file source publishes each file whole; the converter turns CSV
      // (header row = column names) into JSON records, which the database
      // destination inserts one row per record.
      { id: 'src', type: 'consumer', config: { type: 'file', file: { path: '/data/input/csv' } } },
      { id: 'conv', type: 'converter', config: { input_format: 'csv' } },
      {
        id: 'dst',
        type: 'producer',
        config: {
          type: 'database',
          database: { host: '', port: 5432, user: '', password: '', database: '', table: '', mode: 'insert' },
        },
      },
    ],
    edges: [
      { source: 'src', target: 'conv' },
      { source: 'conv', target: 'dst' },
    ],
    fields: [
      {
        nodeId: 'src',
        objectPath: 'file',
        key: 'path',
        label: 'Folder to watch',
        help: 'Every .csv file that lands here is loaded. Its first row must name the columns.',
        placeholder: '/data/input/csv',
      },
      {
        nodeId: 'dst',
        objectPath: 'database',
        key: 'host',
        label: 'Database host',
        help: 'Host name of the PostgreSQL server the rows go to.',
        placeholder: 'db.example.com',
      },
      {
        nodeId: 'dst',
        objectPath: 'database',
        key: 'port',
        label: 'Port',
        help: 'Usually 5432.',
        numeric: true,
        placeholder: '5432',
      },
      {
        nodeId: 'dst',
        objectPath: 'database',
        key: 'database',
        label: 'Database name',
        help: 'The database that holds the target table.',
        placeholder: 'analytics',
      },
      {
        nodeId: 'dst',
        objectPath: 'database',
        key: 'user',
        label: 'User',
        help: 'A database user allowed to insert into the table.',
        placeholder: 'vrsky_loader',
      },
      {
        nodeId: 'dst',
        objectPath: 'database',
        key: 'password',
        label: 'Password',
        help: 'Stored encrypted — never saved in plain text.',
        secret: true,
        placeholder: 'password',
      },
      {
        nodeId: 'dst',
        objectPath: 'database',
        key: 'table',
        label: 'Target table',
        help: 'The table each CSV row is inserted into, without a schema prefix (it is looked up in the public schema); its columns must match the CSV header.',
        placeholder: 'imported_rows',
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
      {
        id: 'src',
        type: 'consumer',
        config: {
          type: 'api',
          api: {
            base_url: '',
            endpoints: [{ path: '/', params: '', auth_type: 'none', auth_value: '' }],
            poll_interval_seconds: 60,
          },
        },
      },
      { id: 'dst', type: 'producer', config: { type: 'file', file: { path: '/data/output' } } },
    ],
    edges: [{ source: 'src', target: 'dst' }],
    fields: [
      {
        nodeId: 'src',
        objectPath: 'api',
        key: 'base_url',
        label: 'API base URL',
        help: 'The scheme and host of a REST API returning JSON, without the path.',
        placeholder: 'https://api.example.com',
      },
      {
        nodeId: 'src',
        objectPath: 'api.endpoints.0',
        key: 'path',
        label: 'Endpoint path',
        help: 'The path called on every poll, with any query string, e.g. /v1/items?limit=100.',
        placeholder: '/v1/items',
      },
      {
        nodeId: 'src',
        objectPath: 'api',
        key: 'poll_interval_seconds',
        label: 'Poll interval (seconds)',
        help: 'How often to call the API.',
        numeric: true,
        placeholder: '60',
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
  // --- Retail: Sitoo POS ⇄ Business Central ERP (#435) ----------------------
  {
    id: 'sitoo-to-business-central',
    name: 'POS sales → ERP (Sitoo → Business Central)',
    summary:
      'Beta: verify the mapping on a test company first. Creates a Business Central sales invoice for each Sitoo receipt, one line per item (SKU = BC item number, prices incl. VAT).',
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
        id: 'conv',
        type: 'converter',
        // One Sitoo page in, one salesInvoice per receipt out (split_records),
        // the receipt's items as salesInvoiceLines. Field table and the
        // reasoning behind each choice: plans/issue-435-sitoo-bc-mapping.md.
        config: {
          drop_unmapped: true,
          split_records: true,
          mappings: [
            { type: 'static', source: '', target: 'customerNumber', value: '' },
            { type: 'rename', source: 'receiptnumber', target: 'externalDocumentNumber' },
            {
              type: 'each',
              source: 'items',
              target: 'salesInvoiceLines',
              drop_unmapped: true,
              mappings: [
                { type: 'static', source: '', target: 'lineType', value: 'Item' },
                { type: 'rename', source: 'sku', target: 'lineObjectNumber' },
                { type: 'to_number', source: 'quantity', target: 'quantity' },
                { type: 'to_number', source: 'priceeach', target: 'unitPrice' },
              ],
            },
          ],
        },
      },
      {
        id: 'dst',
        type: 'producer',
        config: {
          type: 'business_central',
          business_central: {
            environment: 'Production',
            entity: 'salesInvoices',
            method: 'POST',
            // The Sitoo source re-reads every receipt on each poll; this skips
            // the ones BC already has. Invoices, not orders: salesInvoices
            // lists drafts and posted invoices alike, while a posted order
            // leaves salesOrders and would be created again on the next poll.
            dedupe_fields: ['externalDocumentNumber'],
          },
        },
      },
    ],
    edges: [
      { source: 'src', target: 'conv' },
      { source: 'conv', target: 'dst' },
    ],
    fields: [
      ...sitooFields('src'),
      ...businessCentralFields('dst'),
      {
        nodeId: 'conv',
        objectPath: 'mappings.0',
        key: 'value',
        label: 'BC customer number for POS sales',
        help: 'Every sales invoice is created for this customer, e.g. a "Store sales" cash customer. Turn on Prices Including VAT on that customer: Sitoo prices include VAT.',
        placeholder: 'C00010',
      },
    ],
  },
  {
    id: 'business-central-to-sitoo',
    name: 'ERP inventory → POS (Business Central → Sitoo)',
    summary:
      'Beta: verify the mapping on a test warehouse first. Sends the stock of every Business Central inventory item to a Sitoo warehouse every 15 minutes (SKU = BC item number, total over all BC locations).',
    icon: '📦',
    sourceLabel: 'Business Central (ERP)',
    destLabel: 'Sitoo (POS)',
    nodes: [
      {
        id: 'src',
        type: 'consumer',
        // Not incremental: an item's lastModifiedDateTime does not move when
        // its stock does, so only a full read sees stock changes.
        config: {
          type: 'business_central',
          business_central: {
            environment: 'Production',
            entity: 'items',
            filter: "type eq 'Inventory'",
            page_size: 500,
            poll_interval_seconds: 900,
          },
        },
      },
      {
        id: 'conv',
        type: 'converter',
        config: {
          drop_unmapped: true,
          mappings: [
            { type: 'rename', source: 'number', target: 'sku' },
            { type: 'to_string', source: 'inventory', target: 'decimaltotal' },
          ],
        },
      },
      {
        id: 'dst',
        type: 'producer',
        config: { type: 'sitoo', sitoo: { resource: '', method: 'POST' } },
      },
    ],
    edges: [
      { source: 'src', target: 'conv' },
      { source: 'conv', target: 'dst' },
    ],
    fields: [
      ...businessCentralFields('src'),
      ...sitooFields('dst'),
      {
        nodeId: 'dst',
        objectPath: 'sitoo',
        key: 'resource',
        label: 'Sitoo warehouse stock path',
        help: 'warehouses/<warehouse id>/warehouseitems, with the id of the Sitoo warehouse whose stock is set.',
        placeholder: 'warehouses/1/warehouseitems',
      },
    ],
  },
]

export function templateById(id: string): PipelineTemplate | undefined {
  return TEMPLATES.find((t) => t.id === id)
}
