/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_API_URL: string
  readonly VITE_WS_URL: string
  readonly VITE_TENANT_ID: string
  readonly VITE_LOG_LEVEL: 'debug' | 'info' | 'warn' | 'error'
  readonly VITE_FILE_PRODUCER_URL: string
  readonly VITE_FILE_PRODUCER_TOKEN: string
  readonly VITE_WEBHOOK_INGRESS_URL: string
  readonly VITE_DOCS_URL: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
