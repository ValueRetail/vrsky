import type { ConnectionFormData } from '../../../types/forms'

interface BasicInfoStepProps {
  formData: ConnectionFormData
  onChange: (field: 'basicInfo', data: unknown) => void
}

export default function BasicInfoStep({ formData, onChange }: BasicInfoStepProps) {
  const handleChange = (field: 'name' | 'description', value: string) => {
    onChange('basicInfo', {
      ...formData.basicInfo,
      [field]: value,
    })
  }

  return (
    <div className="space-y-6">
      <div>
        <label className="block text-sm font-medium text-gray-900 mb-2">
          Connection Name *
        </label>
        <input
          type="text"
          value={formData.basicInfo.name}
          onChange={e => handleChange('name', e.target.value)}
          placeholder="e.g., Customer Data Pipeline"
          className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500"
        />
        <p className="text-xs text-gray-600 mt-1">A unique name for this connection</p>
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-900 mb-2">
          Description
        </label>
        <textarea
          value={formData.basicInfo.description}
          onChange={e => handleChange('description', e.target.value)}
          placeholder="Optional description of what this connection does"
          rows={4}
          className="w-full px-4 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500"
        />
        <p className="text-xs text-gray-600 mt-1">Optional description for documentation</p>
      </div>
    </div>
  )
}
