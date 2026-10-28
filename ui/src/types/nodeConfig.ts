/**
 * Shapes of the node-config sub-objects the builder and the property editor
 * read. On the wire a node config is a free-form Record<string, unknown>;
 * these name the keys the UI touches so the code does not cast to `any`.
 */
export type FileCfg = { path?: string }
export type HttpCfg = { url?: string; method?: string; auth_type?: string; oauth_grant_id?: string }
export type DbCfg = { host?: string; port?: number | string; database?: string; table?: string; query?: string }

/**
 * The config keys each connector actually reads, for the connectors the
 * onboarding templates use (plans/template-fields.md). Taken from the Go
 * config structs (`json:"…"` tags in src/cmd/<connector>/) and the backend's
 * required-field rules (nodeConfigRules in src/pkg/managementapi/nodeconfig.go).
 *
 * `block` is the settings object a source or destination keeps its keys in
 * (config.http, config.file, …); a converter keeps them at the root, so its
 * block is ''. `required` are keys the backend refuses to start without, or
 * without which the connector does nothing. `numeric` keys are ints on the Go
 * side: a JSON string there fails to unmarshal.
 *
 * templates.test.ts checks every template against this table, so a template
 * that writes a key its connector ignores fails the build instead of deploying
 * a pipeline that silently does nothing. Add a connector here before using it
 * in a template.
 */
export type ConnectorKeys = {
  block: string
  keys: readonly string[]
  required: readonly string[]
  numeric: readonly string[]
}

export const CONNECTOR_KEYS: Record<string, ConnectorKeys> = {
  // webhook-consumer: listens; nothing to configure.
  'consumer/http': { block: 'http', keys: [], required: [], numeric: [] },
  // http-producer (src/cmd/http-producer)
  'producer/http': {
    block: 'http',
    keys: ['url', 'method', 'headers', 'auth_type', 'oauth_grant_id', 'tls'],
    required: ['url'],
    numeric: [],
  },
  // file-consumer: watches path inside the tenant's folder.
  'consumer/file': { block: 'file', keys: ['path'], required: ['path'], numeric: [] },
  // file-producer: path may be empty (the mounted default).
  'producer/file': { block: 'file', keys: ['path', 'file_pattern'], required: [], numeric: [] },
  // api-consumer (APIConsumerConfig in src/cmd/api-consumer/service.go)
  'consumer/api': {
    block: 'api',
    keys: ['base_url', 'endpoints', 'poll_interval_seconds', 'one_time_only'],
    required: ['base_url', 'endpoints'],
    numeric: ['poll_interval_seconds'],
  },
  // db-producer (TargetDBConfig in src/cmd/db-producer/main.go)
  'producer/database': {
    block: 'database',
    keys: ['host', 'port', 'user', 'password', 'database', 'table', 'sslmode', 'mode'],
    required: ['host', 'table'],
    numeric: ['port'],
  },
  // sitoo-consumer (SitooConfig in src/cmd/sitoo-consumer/service.go). The
  // ids are int64; api_password is replaced by api_password_secret_id at deploy.
  'consumer/sitoo': {
    block: 'sitoo',
    keys: [
      'account_id', 'site_id', 'api_id', 'api_password', 'base_url', 'resource',
      'poll_interval_seconds', 'page_size', 'webhook_raw_event', 'webhook_id_field',
    ],
    required: ['account_id', 'site_id', 'api_id', 'api_password'],
    numeric: ['account_id', 'site_id', 'poll_interval_seconds', 'page_size'],
  },
  // sitoo-producer (SitooProducerConfig in src/cmd/sitoo-producer/service.go)
  'producer/sitoo': {
    block: 'sitoo',
    keys: ['account_id', 'site_id', 'api_id', 'api_password', 'base_url', 'resource', 'method'],
    required: ['account_id', 'site_id', 'api_id', 'api_password'],
    numeric: ['account_id', 'site_id'],
  },
  // business-central-consumer (BCConfig in src/cmd/business-central-consumer/service.go)
  'consumer/business_central': {
    block: 'business_central',
    keys: [
      'aad_tenant_id', 'environment', 'company_id', 'client_id', 'client_secret', 'entity', 'filter',
      'incremental', 'cursor_field', 'page_size', 'api_base_url', 'token_url', 'scope',
      'poll_interval_seconds', 'pictures',
    ],
    required: ['aad_tenant_id', 'company_id', 'client_id', 'client_secret'],
    numeric: ['page_size', 'poll_interval_seconds'],
  },
  // business-central-producer (BCProducerConfig in src/cmd/business-central-producer/service.go)
  'producer/business_central': {
    block: 'business_central',
    keys: [
      'aad_tenant_id', 'environment', 'company_id', 'client_id', 'client_secret', 'entity', 'method',
      'dedupe_fields', 'api_base_url', 'token_url', 'scope',
    ],
    required: ['aad_tenant_id', 'company_id', 'client_id', 'client_secret'],
    numeric: [],
  },
  // data-converter (ConverterNodeConfig in src/cmd/data-converter/main.go)
  converter: {
    block: '',
    keys: [
      'mappings', 'drop_unmapped', 'split_records',
      'input_format', 'input_csv_delimiter', 'input_csv_no_header', 'input_csv_trim_space',
      'input_xml_record_path', 'input_xml_attr_prefix', 'input_xml_text_key',
      'output_format', 'csv_delimiter', 'csv_headers', 'text_template', 'xml_root_tag', 'xml_row_tag',
    ],
    required: [],
    numeric: [],
  },
}

/**
 * The mapping types the data-converter implements (applyMappings in
 * src/cmd/data-converter/main.go). Any other type is skipped without a word,
 * so a template using one would silently drop that field. `each` maps every
 * object of an array field with its own nested `mappings`.
 */
export const CONVERTER_MAPPING_TYPES: readonly string[] = [
  'rename', 'copy', 'static', 'remove', 'template', 'to_string', 'to_number', 'each',
]

/** The CONNECTOR_KEYS entry for a node, or undefined when the table lacks it. */
export function connectorKeysFor(nodeType: string, config: Record<string, unknown>): ConnectorKeys | undefined {
  if (nodeType === 'converter' || nodeType === 'filter') return CONNECTOR_KEYS[nodeType]
  return CONNECTOR_KEYS[`${nodeType}/${String(config.type ?? '')}`]
}
