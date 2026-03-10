import type { ConnectionFormData, FilterStepData, FilterRulesFormData, WasmScriptFormData } from '../../../types/forms'

interface FilterStepProps {
  formData: ConnectionFormData
  onChange: (field: 'filter', data: FilterStepData) => void
}

export default function FilterStep({ formData, onChange }: FilterStepProps) {
  const filterType = formData.filter.type
  const config = formData.filter.config
  const rulesConfig = filterType === 'rules' ? (config as FilterRulesFormData) : ({} as Partial<FilterRulesFormData>)
  const wasmConfig = filterType === 'wasm' ? (config as WasmScriptFormData) : ({} as Partial<WasmScriptFormData>)

  const handleTypeChange = (type: 'rules' | 'wasm') => {
    let newConfig: FilterRulesFormData | WasmScriptFormData

    switch (type) {
      case 'rules':
        newConfig = {
          rules: [],
        }
        break
      case 'wasm':
        newConfig = {
          script: '',
        }
        break
    }

    onChange('filter', {
      type,
      config: newConfig,
    })
  }

  const handleFieldChange = (field: string, value: unknown) => {
    onChange('filter', {
      ...formData.filter,
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
          Filter Type *
        </label>
        <select
          value={filterType}
          onChange={e => handleTypeChange(e.target.value as 'rules' | 'wasm')}
          className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500"
        >
          <option value="rules">Filter Rules</option>
          <option value="wasm">WASM Script</option>
        </select>
      </div>

      {filterType === 'rules' && (
        <div>
          <label className="block text-sm font-medium text-gray-900 mb-2">
            Filtering Rules
          </label>
          <div className="border border-gray-300 rounded-md p-4 bg-gray-50">
            <p className="text-sm text-gray-600 mb-4">
              Define conditions to filter messages. Messages matching all conditions will pass.
            </p>
            <textarea
              value={JSON.stringify(rulesConfig.rules || [], null, 2)}
              onChange={e => {
                try {
                  const rules = JSON.parse(e.target.value)
                  handleFieldChange('rules', rules)
                } catch {
                  // Allow invalid JSON while editing
                }
              }}
              placeholder='[{"field": "status", "operator": "eq", "value": "active"}]'
              rows={8}
              className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500 font-mono text-sm"
            />
          </div>
        </div>
      )}

      {filterType === 'wasm' && (
        <div>
          <label className="block text-sm font-medium text-gray-900 mb-2">
            WASM Script
          </label>
          <p className="text-sm text-gray-600 mb-2">
            Provide compiled WASM module code for custom filtering logic
          </p>
          <textarea
            value={wasmConfig.script || ''}
            onChange={e => handleFieldChange('script', e.target.value)}
            placeholder="Base64-encoded WASM binary"
            rows={10}
            className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500 font-mono text-sm"
          />
        </div>
      )}
    </div>
  )
}
