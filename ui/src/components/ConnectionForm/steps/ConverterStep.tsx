import type { ConnectionFormData, ConverterStepData } from '../../../types/forms'

interface ConverterStepProps {
  formData: ConnectionFormData
  onChange: (field: 'converter', data: ConverterStepData) => void
}

export default function ConverterStep({ formData, onChange }: ConverterStepProps) {
  const converterType = formData.converter.type
  const config = formData.converter.config as any

  const handleTypeChange = (type: 'schema' | 'mapper' | 'rules') => {
    let newConfig: any = {}

    switch (type) {
      case 'schema':
        newConfig = {
          inputSchema: {},
        }
        break
      case 'mapper':
        newConfig = {
          mappings: [],
        }
        break
      case 'rules':
        newConfig = {
          rules: [],
        }
        break
    }

    onChange('converter', {
      type,
      config: newConfig,
    })
  }

  const handleFieldChange = (field: string, value: unknown) => {
    onChange('converter', {
      ...formData.converter,
      config: {
        ...config,
        [field]: value,
      },
    })
  }

  return (
    <div className="space-y-6">
      <div>
        <label className="block text-sm font-medium text-gray-900 mb-2">
          Converter Type *
        </label>
        <select
          value={converterType}
          onChange={e => handleTypeChange(e.target.value as 'schema' | 'mapper' | 'rules')}
          className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500"
        >
          <option value="schema">Schema Validator</option>
          <option value="mapper">Field Mapper</option>
          <option value="rules">Rule Engine</option>
        </select>
      </div>

      {converterType === 'schema' && (
        <div>
          <label className="block text-sm font-medium text-gray-900 mb-2">
            Input Schema (JSON)
          </label>
          <textarea
            value={JSON.stringify(config.inputSchema || {}, null, 2)}
            onChange={e => {
              try {
                const schema = JSON.parse(e.target.value)
                handleFieldChange('inputSchema', schema)
              } catch {
                // Allow invalid JSON while editing
              }
            }}
            placeholder='{"type": "object", "properties": {...}}'
            rows={8}
            className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500 font-mono text-sm"
          />
          <p className="text-xs text-gray-600 mt-1">Define the JSON Schema for input validation</p>
        </div>
      )}

      {converterType === 'mapper' && (
        <div>
          <label className="block text-sm font-medium text-gray-900 mb-2">
            Field Mappings
          </label>
          <div className="border border-gray-300 rounded-md p-4 bg-gray-50">
            <p className="text-sm text-gray-600 mb-4">
              Define source field to target field mappings
            </p>
            <textarea
              value={JSON.stringify(config.mappings || [], null, 2)}
              onChange={e => {
                try {
                  const mappings = JSON.parse(e.target.value)
                  handleFieldChange('mappings', mappings)
                } catch {
                  // Allow invalid JSON while editing
                }
              }}
              placeholder='[{"sourceField": "id", "targetField": "user_id"}]'
              rows={6}
              className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500 font-mono text-sm"
            />
          </div>
        </div>
      )}

      {converterType === 'rules' && (
        <div>
          <label className="block text-sm font-medium text-gray-900 mb-2">
            Transformation Rules
          </label>
          <div className="border border-gray-300 rounded-md p-4 bg-gray-50">
            <p className="text-sm text-gray-600 mb-4">
              Define rules for data transformation
            </p>
            <textarea
              value={JSON.stringify(config.rules || [], null, 2)}
              onChange={e => {
                try {
                  const rules = JSON.parse(e.target.value)
                  handleFieldChange('rules', rules)
                } catch {
                  // Allow invalid JSON while editing
                }
              }}
              placeholder='[{"condition": "...", "action": "..."}]'
              rows={6}
              className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500 font-mono text-sm"
            />
          </div>
        </div>
      )}
    </div>
  )
}
