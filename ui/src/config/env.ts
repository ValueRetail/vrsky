/**
 * Environment configuration loader
 * Validates and provides typed access to environment variables
 */

const getEnv = (key: string, defaultValue?: string): string => {
  const value = import.meta.env[key as keyof ImportMetaEnv] ?? defaultValue
  if (!value) {
    throw new Error(`Environment variable ${key} is not defined`)
  }
  return value
}

export const config = {
  apiUrl: getEnv('VITE_API_URL', 'http://localhost:3000'),
  wsUrl: getEnv('VITE_WS_URL', 'http://localhost:3000'),
  tenantId: getEnv('VITE_TENANT_ID', 'tenant-1'),
  logLevel: (getEnv('VITE_LOG_LEVEL', 'info') as 'debug' | 'info' | 'warn' | 'error'),
  isDev: import.meta.env.DEV,
  isProd: import.meta.env.PROD,
}

// Validate configuration on load
export const validateConfig = () => {
  if (!config.apiUrl) {
    throw new Error('VITE_API_URL is required')
  }
  if (!config.tenantId) {
    throw new Error('VITE_TENANT_ID is required')
  }
}
