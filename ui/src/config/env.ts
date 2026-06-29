/**
 * Environment configuration loader
 * Validates and provides typed access to environment variables
 */

const getEnv = (key: string, defaultValue?: string): string => {
  const value = import.meta.env[key as keyof ImportMetaEnv] ?? defaultValue
  if (value === undefined) {
    throw new Error(`Environment variable ${key} is not defined`)
  }
  return value
}

// apiUrl defaults to '' — relative paths go through Vite's /api proxy in dev
// (same-origin → cookies behave) and through Traefik in prod where the UI
// and API share one origin. Override with VITE_API_URL only when running
// the UI against a remote API that doesn't share an origin.
export const config = {
  apiUrl: getEnv('VITE_API_URL', ''),
  wsUrl: getEnv('VITE_WS_URL', 'http://localhost:3000'),
  tenantId: getEnv('VITE_TENANT_ID', 'tenant-1'),
  logLevel: (getEnv('VITE_LOG_LEVEL', 'info') as 'debug' | 'info' | 'warn' | 'error'),
  isDev: import.meta.env.DEV,
  isProd: import.meta.env.PROD,
  fileProducerUrl: getEnv('VITE_FILE_PRODUCER_URL', 'http://localhost:9900'),
  // Public base URL of the webhook-consumer ingress. The onboarding wizard (#93)
  // surfaces `${webhookIngressUrl}/webhook/{id}` and POSTs the sample event to
  // it. Defaults to the local compose port; override in deployments where
  // webhooks enter via the gateway/public host.
  webhookIngressUrl: getEnv('VITE_WEBHOOK_INGRESS_URL', 'http://localhost:9100'),
  // Base URLs for the per-worker live-test event streams (SSE) and the file
  // upload endpoint surfaced by the builder's test panels. These hit worker
  // aux ports directly, so they default to the local compose ports; override
  // per deployment (where workers aren't reachable from the browser the panels
  // simply won't connect). Centralized here instead of hardcoding
  // "http://localhost:..." inline so they don't silently break off-localhost.
  fileConsumerUrl: getEnv('VITE_FILE_CONSUMER_URL', 'http://localhost:9200'),
  httpProducerUrl: getEnv('VITE_HTTP_PRODUCER_URL', 'http://localhost:9400'),
  dbProducerUrl: getEnv('VITE_DB_PRODUCER_URL', 'http://localhost:9500'),
  converterUrl: getEnv('VITE_CONVERTER_URL', 'http://localhost:9600'),
  filterUrl: getEnv('VITE_FILTER_URL', 'http://localhost:9700'),
  // Where the "Help & Docs" sidebar link points (the published mkdocs site).
  // Override per deployment; defaults to the GitHub Pages build.
  docsUrl: getEnv('VITE_DOCS_URL', 'https://valueretail.github.io/vrsky/'),
  // Optional bearer token for the file-producer's /files API. Empty in local
  // dev (the server leaves auth disabled); set in deployments that enable
  // FILE_PRODUCER_AUTH_TOKEN on the file-producer. Read directly because an
  // empty value is valid here (getEnv would reject it).
  fileProducerToken: (import.meta.env.VITE_FILE_PRODUCER_TOKEN as string | undefined) ?? '',
}

// Validate configuration on load
export const validateConfig = () => {
  // apiUrl="" is valid (relative). Just check it's been defined explicitly.
  if (config.apiUrl === undefined) {
    throw new Error('VITE_API_URL must be defined (use "" for relative paths)')
  }
  if (!config.tenantId) {
    throw new Error('VITE_TENANT_ID is required')
  }
}
