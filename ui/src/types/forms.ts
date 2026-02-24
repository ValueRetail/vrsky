/**
 * Form Data Types
 * Structures for form state and submission
 */

import type {
  SourceType,
  ConverterType,
  FilterType,
  DestinationType,
} from './models'

// Basic Info Step
export interface BasicInfoFormData {
  name: string
  description: string  // Empty string by default, never undefined
}

// Source Config Forms
export interface HttpSourceFormData {
  url: string
  method: 'GET' | 'POST' | 'PUT' | 'DELETE'
  headers?: Record<string, string>
  auth?: {
    type: 'bearer' | 'basic'
    credentials: string
  }
  timeout?: number
  maxAttempts?: number
  backoffMs?: number
}

export interface FileSourceFormData {
  path: string
  format: 'json' | 'csv' | 'xml'
  encoding?: string
  watch?: boolean
  pollIntervalMs?: number
}

export interface DatabaseSourceFormData {
  connectionString: string
  query: string
  pollingIntervalMs?: number
}

export type SourceFormData = HttpSourceFormData | FileSourceFormData | DatabaseSourceFormData

export interface SourceStepData {
  type: SourceType
  config: SourceFormData
}

// Converter Config Forms
export interface SchemaValidatorFormData {
  inputSchema: Record<string, unknown>
  validationRules?: Record<string, unknown>
}

export interface FieldMapperFormData {
  mappings: Array<{
    sourceField: string
    targetField: string
    transform?: string
  }>
}

export interface RuleEngineFormData {
  rules: Array<{
    condition: string
    action: string
  }>
}

export type ConverterFormData = SchemaValidatorFormData | FieldMapperFormData | RuleEngineFormData

export interface ConverterStepData {
  type: ConverterType
  config: ConverterFormData
}

// Filter Config Forms
export interface FilterRulesFormData {
  rules: Array<{
    field: string
    operator: 'eq' | 'ne' | 'gt' | 'lt' | 'in' | 'nin'
    value: unknown
  }>
}

export interface WasmScriptFormData {
  script: string
  params?: Record<string, unknown>
}

export type FilterFormData = FilterRulesFormData | WasmScriptFormData

export interface FilterStepData {
  type: FilterType
  config: FilterFormData
}

// Destination Config Forms
export interface HttpDestinationFormData {
  url: string
  method: 'GET' | 'POST' | 'PUT' | 'DELETE'
  headers?: Record<string, string>
  auth?: {
    type: 'bearer' | 'basic'
    credentials: string
  }
  timeout?: number
  maxAttempts?: number
  backoffMs?: number
}

export interface FileDestinationFormData {
  path: string
  format: 'json' | 'csv' | 'xml'
  encoding?: string
  append?: boolean
}

export interface DatabaseDestinationFormData {
  connectionString: string
  table: string
  operation: 'insert' | 'update' | 'upsert'
}

export type DestinationFormData = HttpDestinationFormData | FileDestinationFormData | DatabaseDestinationFormData

export interface DestinationStepData {
  type: DestinationType
  config: DestinationFormData
}

// Complete Wizard Form Data
export interface ConnectionFormData {
  basicInfo: BasicInfoFormData
  source: SourceStepData
  converter: ConverterStepData
  filter: FilterStepData
  destination: DestinationStepData
}

// Test Data Forms
export interface SendTestMessageFormData {
  connectionId: string
  message: string
}

export interface AutoGeneratorFormData {
  connectionId: string
  rate: number // messages per second
  messageSize: 'small' | 'medium' | 'large'
}

// Filter and Search Forms
export interface ConnectionFilterFormData {
  searchQuery?: string
  status?: 'running' | 'stopped' | 'error' | 'all'
  sourceType?: string
  destinationType?: string
  dateFrom?: string
  dateTo?: string
}

export interface MessageFilterFormData {
  connectionId: string
  component?: 'consumer' | 'converter' | 'filter' | 'producer'
  startDate?: string
  endDate?: string
  searchQuery?: string
}

// Form Validation Error
export interface FormValidationError {
  field: string
  message: string
}

// Form State
export interface FormState {
  currentStep: number
  totalSteps: number
  data: Partial<ConnectionFormData>
  errors: FormValidationError[]
  isSubmitting: boolean
  isValid: boolean
}
