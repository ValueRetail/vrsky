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
 * Not in the catalog (plans/template-fields.md): Sitoo ⇄ Business Central. Both
 * directions need a tested field mapping between the two systems' records; a
 * template that wires them straight together deploys, runs, and has every
 * message rejected by the target API.
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
]

export function templateById(id: string): PipelineTemplate | undefined {
  return TEMPLATES.find((t) => t.id === id)
}
