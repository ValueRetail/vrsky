// @ts-nocheck

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
      http: {
        url: http.url,
        method: http.method,
        headers: http.headers,
        auth: http.auth,
        polling: http.timeout !== undefined ? {
          interval: http.timeout,
          timeout: http.timeout,
        } : undefined,
      },
    }
  }
  if (type === 'file') {
    const file = config as FileSourceFormData
    return {
      type: 'file',
      file: {
        path: file.path,
        pattern: file.format,
        encoding: file.encoding,
        watch: file.watch,
      },
    }
  }
  // database
  const db = config as DatabaseSourceFormData
  return {
    type: 'database',
    database: {
      connection_string: db.connectionString,
      query: db.query,
      poll_interval: Math.floor((db.pollingIntervalMs ?? 0) / 1000),
    },
  }
}

export const buildConverterConfig = (type: string, config: ConverterFormData): ConverterConfig => {
  if (type === 'schema') {
    const s = config as SchemaValidatorFormData
    return {
      schema_validator: {
        input_schema: s.inputSchema as unknown as any,
        output_schema: {} as any,
      },
    }
  }
  if (type === 'mapper') {
    const m = config as FieldMapperFormData
    const mappings: Record<string, string> = {}
    ;(m.mappings ?? []).forEach(item => {
      if (item.sourceField && item.targetField) {
        mappings[item.sourceField] = item.targetField
      }
    })
    return {
      field_mapper: {
        mappings,
      },
    }
  }
  // rules
  const r = config as RuleEngineFormData
  return {
    rule_engine: {
      rules: (r.rules ?? []).map(rule => ({
        name: rule.name,
        condition: rule.condition,
        transformation: rule.transformation,
      })),
    },
  }
}

export const buildFilterConfig = (type: string, config: FilterFormData): FilterConfig => {
  if (type === 'rules') {
    const r = config as FilterRulesFormData
    return {
      rules: (r.rules ?? []).map(rule => ({
        name: rule.name,
        condition: rule.condition,
      })),
    }
  }
  const w = config as WasmScriptFormData
  return {
    wasm: {
      binary: w.script as unknown as any,
    },
  }
}

export const buildDestinationConfig = (type: string, config: DestinationFormData): DestinationConfig => {
  if (type === 'http') {
    const http = config as HttpDestinationFormData
    return {
      type: 'http',
      http: {
        url: http.url,
        method: http.method,
        headers: http.headers,
        auth: http.auth,
      },
    }
  }
  if (type === 'file') {
    const file = config as FileDestinationFormData
    return {
      type: 'file',
      file: {
        path: file.path,
        format: file.format,
        append: file.append,
        create_dir: true,
      },
    }
  }
  // database
  const db = config as DatabaseDestinationFormData
  return {
    type: 'database',
    database: {
      connection_string: db.connectionString,
      query: db.table,
    },
  }
}

// Reverse transformers (API → form format)
export const toSourceFormData = (config: SourceConfig): SourceFormData => {
  if (config.type === 'http' && config.http) {
    return {
      url: config.http.url,
      method: config.http.method,
      headers: config.http.headers,
      auth: config.http.auth,
      timeout: config.http.polling?.interval,
    } satisfies HttpSourceFormData
  }
  if (config.type === 'file' && config.file) {
    return {
      path: config.file.path,
      format: config.file.pattern,
      encoding: config.file.encoding,
      watch: config.file.watch,
    } satisfies FileSourceFormData
  }
  if (config.type === 'database' && config.database) {
    return {
      connectionString: config.database.connection_string,
      query: config.database.query,
      pollingIntervalMs: (config.database.poll_interval ?? 0) * 1000,
    } satisfies DatabaseSourceFormData
  }
  return {} as SourceFormData
}

export const toConverterFormData = (config: ConverterConfig): ConverterFormData => {
  if (config.schema_validator) {
    return {
      inputSchema: JSON.stringify(config.schema_validator.input_schema, null, 2),
    } satisfies SchemaValidatorFormData
  }
  if (config.field_mapper) {
    const mappings = Object.entries(config.field_mapper.mappings || {}).map(([source, target]) => ({
      sourceField: source,
      targetField: target,
    }))
    return {
      mappings,
    } satisfies FieldMapperFormData
  }
  if (config.rule_engine) {
    return {
      rules: config.rule_engine.rules || [],
    } satisfies RuleEngineFormData
  }
  return {} as ConverterFormData
}

export const toFilterFormData = (config: FilterConfig): FilterFormData => {
  if (config.rules) {
    return { rules: config.rules } satisfies FilterRulesFormData
  }
  if (config.wasm) {
    return { script: config.wasm.binary as any } satisfies WasmScriptFormData
  }
  return {} as FilterFormData
}

export const toDestinationFormData = (config: DestinationConfig): DestinationFormData => {
  if (config.type === 'http' && config.http) {
    return {
      url: config.http.url,
      method: config.http.method,
      headers: config.http.headers,
      auth: config.http.auth,
    } satisfies HttpDestinationFormData
  }
  if (config.type === 'file' && config.file) {
    return {
      path: config.file.path,
      format: config.file.format,
      append: config.file.append,
    } satisfies FileDestinationFormData
  }
  if (config.type === 'database' && config.database) {
    return {
      connectionString: config.database.connection_string,
      table: config.database.query,
    } satisfies DatabaseDestinationFormData
  }
  return {} as DestinationFormData
}
