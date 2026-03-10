import type { ConnectionFormData, HttpSourceFormData, FileSourceFormData, DatabaseSourceFormData, SourceStepData } from '../../../types/forms'

interface SourceStepProps {
  formData: ConnectionFormData
  onChange: (field: 'source', data: SourceStepData) => void
}

export default function SourceStep({ formData, onChange }: SourceStepProps) {
  const sourceType = formData.source.type
  const config = formData.source.config

  const handleTypeChange = (type: 'http' | 'file' | 'database') => {
    let newConfig: any = {}

    switch (type) {
      case 'http':
        newConfig = {
          url: '',
          method: 'GET',
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
          watch: false,
          pollIntervalMs: 5000,
        }
        break
      case 'database':
        newConfig = {
          connectionString: '',
          query: '',
          pollingIntervalMs: 10000,
        }
        break
    }

    onChange('source', {
      type,
      config: newConfig,
    })
  }

  const handleFieldChange = (field: string, value: unknown) => {
    onChange('source', {
      ...formData.source,
      config: {
        ...config,
        [field]: value,
      },
    })
  }

  const httpConfig = config as any as HttpSourceFormData | Partial<HttpSourceFormData>
  const fileConfig = config as any as FileSourceFormData | Partial<FileSourceFormData>
  const dbConfig = config as any as DatabaseSourceFormData | Partial<DatabaseSourceFormData>

  return (
    <div className="space-y-6">
      <div>
        <label className="label">
          Source Type *
        </label>
        <select
          value={sourceType}
          onChange={e => handleTypeChange(e.target.value as 'http' | 'file' | 'database')}
          className="input-base"
        >
          <option value="http">HTTP Webhook</option>
          <option value="file">File System</option>
          <option value="database">Database</option>
        </select>
      </div>

      {sourceType === 'http' && (
        <>
          <div>
            <label className="label">
              URL *
            </label>
            <input
              type="text"
              value={httpConfig.url || ''}
              onChange={e => handleFieldChange('url', e.target.value)}
              placeholder="https://api.example.com/webhook"
              className="input-base"
            />
          </div>

          <div>
            <label className="label">
              Method
            </label>
            <select
              value={httpConfig.method || 'GET'}
              onChange={e => handleFieldChange('method', e.target.value)}
              className="input-base"
            >
              <option value="GET">GET</option>
              <option value="POST">POST</option>
              <option value="PUT">PUT</option>
              <option value="DELETE">DELETE</option>
            </select>
          </div>

          <div>
            <label className="label">
              Timeout (seconds)
            </label>
            <input
              type="number"
              value={httpConfig.timeout || 30}
              onChange={e => handleFieldChange('timeout', parseInt(e.target.value) || 30)}
              min="1"
              className="input-base"
            />
          </div>

          <div>
            <label className="label">
              Max Attempts
            </label>
            <input
              type="number"
              value={httpConfig.maxAttempts || 3}
              onChange={e => handleFieldChange('maxAttempts', parseInt(e.target.value) || 3)}
              min="0"
              max="10"
              className="input-base"
            />
          </div>
        </>
      )}

      {sourceType === 'file' && (
        <>
          <div>
            <label className="label">
              File Path *
            </label>
            <input
              type="text"
              value={fileConfig.path || ''}
              onChange={e => handleFieldChange('path', e.target.value)}
              placeholder="/data/input.json"
              className="input-base"
            />
          </div>

          <div>
            <label className="label">
              Format
            </label>
            <select
              value={fileConfig.format || 'json'}
              onChange={e => handleFieldChange('format', e.target.value)}
              className="input-base"
            >
              <option value="json">JSON</option>
              <option value="csv">CSV</option>
              <option value="xml">XML</option>
            </select>
          </div>

          <div>
            <label className="label">
              Poll Interval (milliseconds)
            </label>
            <input
              type="number"
              value={fileConfig.pollIntervalMs || 5000}
              onChange={e => handleFieldChange('pollIntervalMs', parseInt(e.target.value) || 5000)}
              min="1000"
              step="1000"
              className="input-base"
            />
          </div>

          <div className="flex items-center gap-3">
            <input
              type="checkbox"
              id="watch"
              checked={fileConfig.watch || false}
              onChange={e => handleFieldChange('watch', e.target.checked)}
              className="w-4 h-4 rounded border-neutral-300 dark:border-neutral-600 text-primary-600 dark:text-primary-400"
            />
            <label htmlFor="watch" className="text-sm font-medium text-neutral-900 dark:text-neutral-50 cursor-pointer">
              Watch for changes
            </label>
          </div>
        </>
      )}

      {sourceType === 'database' && (
        <>
          <div>
            <label className="label">
              Connection String *
            </label>
            <input
              type="text"
              value={dbConfig.connectionString || ''}
              onChange={e => handleFieldChange('connectionString', e.target.value)}
              placeholder="postgresql://user:pass@localhost/db"
              className="input-base"
            />
          </div>

          <div>
            <label className="label">
              Query *
            </label>
            <textarea
              value={dbConfig.query || ''}
              onChange={e => handleFieldChange('query', e.target.value)}
              placeholder="SELECT * FROM users"
              rows={4}
              className="input-base font-mono text-sm resize-none"
            />
          </div>

          <div>
            <label className="label">
              Polling Interval (milliseconds)
            </label>
            <input
              type="number"
              value={dbConfig.pollingIntervalMs || 10000}
              onChange={e => handleFieldChange('pollingIntervalMs', parseInt(e.target.value) || 10000)}
              min="1000"
              step="1000"
              className="input-base"
            />
          </div>
        </>
      )}
    </div>
  )
}
