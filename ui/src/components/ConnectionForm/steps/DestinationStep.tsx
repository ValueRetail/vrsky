import type { ConnectionFormData, DestinationStepData, HttpDestinationFormData, FileDestinationFormData, DatabaseDestinationFormData } from '../../../types/forms'

interface DestinationStepProps {
  formData: ConnectionFormData
  onChange: (field: 'destination', data: DestinationStepData) => void
}

export default function DestinationStep({ formData, onChange }: DestinationStepProps) {
  const destType = formData.destination.type
  const config = formData.destination.config
  const httpConfig = destType === 'http' ? (config as HttpDestinationFormData) : ({} as Partial<HttpDestinationFormData>)
  const fileConfig = destType === 'file' ? (config as FileDestinationFormData) : ({} as Partial<FileDestinationFormData>)
  const dbConfig = destType === 'database' ? (config as DatabaseDestinationFormData) : ({} as Partial<DatabaseDestinationFormData>)

  const handleTypeChange = (type: 'http' | 'file' | 'database') => {
    let newConfig: HttpDestinationFormData | FileDestinationFormData | DatabaseDestinationFormData

    switch (type) {
      case 'http':
        newConfig = {
          url: '',
          method: 'POST',
          headers: {},
          timeout: 30,
          maxAttempts: 3,
        }
        break
      case 'file':
        newConfig = {
          path: '',
          format: 'json',
          encoding: 'utf-8',
          append: false,
        }
        break
      case 'database':
        newConfig = {
          connectionString: '',
          table: '',
          operation: 'insert',
        }
        break
    }

    onChange('destination', {
      type,
      config: newConfig,
    })
  }

  const handleFieldChange = (field: string, value: unknown) => {
    onChange('destination', {
      ...formData.destination,
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
          Destination Type *
        </label>
        <select
          value={destType}
          onChange={e => handleTypeChange(e.target.value as 'http' | 'file' | 'database')}
          className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500"
        >
          <option value="http">HTTP Endpoint</option>
          <option value="file">File System</option>
          <option value="database">Database</option>
        </select>
      </div>

      {destType === 'http' && (
        <>
          <div>
            <label className="block text-sm font-medium text-gray-900 mb-2">
              URL *
            </label>
            <input
              type="text"
              value={httpConfig.url || ''}
              onChange={e => handleFieldChange('url', e.target.value)}
              placeholder="https://api.example.com/data"
              className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-900 mb-2">
              Method
            </label>
            <select
              value={httpConfig.method || 'POST'}
              onChange={e => handleFieldChange('method', e.target.value)}
              className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500"
            >
              <option value="GET">GET</option>
              <option value="POST">POST</option>
              <option value="PUT">PUT</option>
              <option value="DELETE">DELETE</option>
            </select>
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-900 mb-2">
              Timeout (seconds)
            </label>
            <input
              type="number"
              value={httpConfig.timeout || 30}
              onChange={e => handleFieldChange('timeout', parseInt(e.target.value) || 30)}
              min="1"
              className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-900 mb-2">
              Max Attempts
            </label>
            <input
              type="number"
              value={httpConfig.maxAttempts || 3}
              onChange={e => handleFieldChange('maxAttempts', parseInt(e.target.value) || 3)}
              min="0"
              max="10"
              className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
          </div>
        </>
      )}

      {destType === 'file' && (
        <>
          <div>
            <label className="block text-sm font-medium text-gray-900 mb-2">
              File Path *
            </label>
            <input
              type="text"
              value={fileConfig.path || ''}
              onChange={e => handleFieldChange('path', e.target.value)}
              placeholder="/data/output.json"
              className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-900 mb-2">
              Format
            </label>
            <select
              value={fileConfig.format || 'json'}
              onChange={e => handleFieldChange('format', e.target.value)}
              className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500"
            >
              <option value="json">JSON</option>
              <option value="csv">CSV</option>
              <option value="xml">XML</option>
            </select>
          </div>

          <div className="flex items-center gap-2">
            <input
              type="checkbox"
              id="append"
              checked={fileConfig.append || false}
              onChange={e => handleFieldChange('append', e.target.checked)}
              className="rounded border-gray-300"
            />
            <label htmlFor="append" className="text-sm font-medium text-gray-900">
              Append to file (instead of overwrite)
            </label>
          </div>
        </>
      )}

      {destType === 'database' && (
        <>
          <div>
            <label className="block text-sm font-medium text-gray-900 mb-2">
              Connection String *
            </label>
            <input
              type="text"
              value={dbConfig.connectionString || ''}
              onChange={e => handleFieldChange('connectionString', e.target.value)}
              placeholder="postgresql://user:pass@localhost/db"
              className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-900 mb-2">
              Table Name *
            </label>
            <input
              type="text"
              value={dbConfig.table || ''}
              onChange={e => handleFieldChange('table', e.target.value)}
              placeholder="users"
              className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-900 mb-2">
              Operation
            </label>
            <select
              value={dbConfig.operation || 'insert'}
              onChange={e => handleFieldChange('operation', e.target.value)}
              className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500"
            >
              <option value="insert">Insert</option>
              <option value="update">Update</option>
              <option value="upsert">Upsert</option>
            </select>
          </div>
        </>
      )}
    </div>
  )
}
