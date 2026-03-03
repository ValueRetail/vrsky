/**
 * Type Guard Functions
 * Utilities for narrowing polymorphic types safely
 */

import type {
  SourceConfig,
  ConverterConfig,
  FilterConfig,
  DestinationConfig,
  HttpSourceConfig,
  FileSourceConfig,
  DatabaseSourceConfig,
  SchemaValidatorConfig,
  FieldMapperConfig,
  RuleEngineConfig,
  FilterRulesConfig,
  WasmScriptConfig,
  HttpDestinationConfig,
  FileDestinationConfig,
  DatabaseDestinationConfig,
} from '../types/models'
import type {
  SourceFormData,
  ConverterFormData,
  FilterFormData,
  DestinationFormData,
  HttpSourceFormData,
  FileSourceFormData,
  DatabaseSourceFormData,
  SchemaValidatorFormData,
  FieldMapperFormData,
  RuleEngineFormData,
  FilterRulesFormData,
  WasmScriptFormData,
  HttpDestinationFormData,
  FileDestinationFormData,
  DatabaseDestinationFormData,
} from '../types/forms'

// Source Config type guards
export const isHttpSourceConfig = (config: unknown): config is HttpSourceConfig => {
  return typeof config === 'object' && config !== null && (config as Record<string, unknown>).type === 'http'
}

export const isFileSourceConfig = (config: unknown): config is FileSourceConfig => {
  return typeof config === 'object' && config !== null && (config as Record<string, unknown>).type === 'file'
}

export const isDatabaseSourceConfig = (config: unknown): config is DatabaseSourceConfig => {
  return typeof config === 'object' && config !== null && (config as Record<string, unknown>).type === 'database'
}

// Converter Config type guards
export const isSchemaValidatorConfig = (config: unknown): config is SchemaValidatorConfig => {
  return typeof config === 'object' && config !== null && (config as Record<string, unknown>).type === 'schema'
}

export const isFieldMapperConfig = (config: unknown): config is FieldMapperConfig => {
  return typeof config === 'object' && config !== null && (config as Record<string, unknown>).type === 'mapper'
}

export const isRuleEngineConfig = (config: unknown): config is RuleEngineConfig => {
  return typeof config === 'object' && config !== null && (config as Record<string, unknown>).type === 'rules'
}

// Filter Config type guards
export const isFilterRulesConfig = (config: unknown): config is FilterRulesConfig => {
  return typeof config === 'object' && config !== null && (config as Record<string, unknown>).type === 'rules'
}

export const isWasmScriptConfig = (config: unknown): config is WasmScriptConfig => {
  return typeof config === 'object' && config !== null && (config as Record<string, unknown>).type === 'wasm'
}

// Destination Config type guards
export const isHttpDestinationConfig = (config: unknown): config is HttpDestinationConfig => {
  return typeof config === 'object' && config !== null && (config as Record<string, unknown>).type === 'http'
}

export const isFileDestinationConfig = (config: unknown): config is FileDestinationConfig => {
  return typeof config === 'object' && config !== null && (config as Record<string, unknown>).type === 'file'
}

export const isDatabaseDestinationConfig = (config: unknown): config is DatabaseDestinationConfig => {
  return typeof config === 'object' && config !== null && (config as Record<string, unknown>).type === 'database'
}

// Configuration builders (form → API format)
export const buildSourceConfig = (type: string, config: SourceFormData): SourceConfig => {
  if (type === 'http') {
    const http = config as HttpSourceFormData
    return {
      type: 'http',
      url: http.url,
      method: http.method,
      headers: http.headers,
      auth: http.auth,
      timeout: http.timeout,
      retry: http.maxAttempts !== undefined ? {
        max_attempts: http.maxAttempts,
        backoff_ms: http.backoffMs ?? 1000,
      } : undefined,
    }
  }
  if (type === 'file') {
    const file = config as FileSourceFormData
    return {
      type: 'file',
      path: file.path,
      format: file.format,
      encoding: file.encoding,
      watch: file.watch,
      poll_interval_ms: file.pollIntervalMs,
    }
  }
  // database
  const db = config as DatabaseSourceFormData
  return {
    type: 'database',
    connection_string: db.connectionString,
    query: db.query,
    polling_interval_ms: db.pollingIntervalMs,
  }
}

export const buildConverterConfig = (type: string, config: ConverterFormData): ConverterConfig => {
  if (type === 'schema') {
    const s = config as SchemaValidatorFormData
    return { type: 'schema', input_schema: s.inputSchema, validation_rules: s.validationRules }
  }
  if (type === 'mapper') {
    const m = config as FieldMapperFormData
    return {
      type: 'mapper',
      mappings: (m.mappings ?? []).map(item => ({
        source_field: item.sourceField,
        target_field: item.targetField,
        transform: item.transform,
      })),
    }
  }
  // rules
  const r = config as RuleEngineFormData
  return { type: 'rules', rules: r.rules ?? [] }
}

export const buildFilterConfig = (type: string, config: FilterFormData): FilterConfig => {
  return {
    type: type as 'rules' | 'wasm',
    ...config,
  } as FilterConfig
}

export const buildDestinationConfig = (type: string, config: DestinationFormData): DestinationConfig => {
  if (type === 'http') {
    const http = config as HttpDestinationFormData
    return {
      type: 'http',
      url: http.url,
      method: http.method,
      headers: http.headers,
      auth: http.auth,
      timeout: http.timeout,
      retry: http.maxAttempts !== undefined ? {
        max_attempts: http.maxAttempts,
        backoff_ms: http.backoffMs ?? 1000,
      } : undefined,
    }
  }
  if (type === 'file') {
    const file = config as FileDestinationFormData
    return { type: 'file', path: file.path, format: file.format, encoding: file.encoding, append: file.append }
  }
  // database
  const db = config as DatabaseDestinationFormData
  return { type: 'database', connection_string: db.connectionString, table: db.table, operation: db.operation }
}

// Reverse transformers (API → form format)
export const toSourceFormData = (config: SourceConfig): SourceFormData => {
  if (config.type === 'http') {
    return {
      url: config.url,
      method: config.method,
      headers: config.headers,
      auth: config.auth,
      timeout: config.timeout,
      maxAttempts: config.retry?.max_attempts,
      backoffMs: config.retry?.backoff_ms,
    } satisfies HttpSourceFormData
  }
  if (config.type === 'file') {
    return {
      path: config.path,
      format: config.format,
      encoding: config.encoding,
      watch: config.watch,
      pollIntervalMs: config.poll_interval_ms,
    } satisfies FileSourceFormData
  }
  return {
    connectionString: config.connection_string,
    query: config.query,
    pollingIntervalMs: config.polling_interval_ms,
  } satisfies DatabaseSourceFormData
}

export const toConverterFormData = (config: ConverterConfig): ConverterFormData => {
  if (config.type === 'schema') {
    return {
      inputSchema: config.input_schema,
      validationRules: config.validation_rules,
    } satisfies SchemaValidatorFormData
  }
  if (config.type === 'mapper') {
    return {
      mappings: (config.mappings ?? []).map(m => ({
        sourceField: m.source_field,
        targetField: m.target_field,
        transform: m.transform,
      })),
    } satisfies FieldMapperFormData
  }
  return { rules: config.rules ?? [] } satisfies RuleEngineFormData
}

export const toFilterFormData = (config: FilterConfig): FilterFormData => {
  if (config.type === 'rules') {
    return { rules: config.rules ?? [] } satisfies FilterRulesFormData
  }
  return { script: config.script, params: config.params } satisfies WasmScriptFormData
}

export const toDestinationFormData = (config: DestinationConfig): DestinationFormData => {
  if (config.type === 'http') {
    return {
      url: config.url,
      method: config.method,
      headers: config.headers,
      auth: config.auth,
      timeout: config.timeout,
      maxAttempts: config.retry?.max_attempts,
      backoffMs: config.retry?.backoff_ms,
    } satisfies HttpDestinationFormData
  }
  if (config.type === 'file') {
    return {
      path: config.path,
      format: config.format,
      encoding: config.encoding,
      append: config.append,
    } satisfies FileDestinationFormData
  }
  return {
    connectionString: config.connection_string,
    table: config.table,
    operation: config.operation,
  } satisfies DatabaseDestinationFormData
}
