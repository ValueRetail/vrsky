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
