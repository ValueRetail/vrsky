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
        <label className="label">
          Connection Name *
        </label>
        <input
          type="text"
          value={formData.basicInfo.name}
          onChange={e => handleChange('name', e.target.value)}
          placeholder="e.g., Customer Data Pipeline"
          className="input-base"
        />
        <p className="text-xs text-neutral-600 dark:text-neutral-400 mt-1">A unique name for this connection</p>
      </div>

      <div>
        <label className="label">
          Description
        </label>
        <textarea
          value={formData.basicInfo.description}
          onChange={e => handleChange('description', e.target.value)}
          placeholder="Optional description of what this connection does"
          rows={4}
          className="input-base resize-none"
        />
        <p className="text-xs text-neutral-600 dark:text-neutral-400 mt-1">Optional description for documentation</p>
      </div>
    </div>
  )
}
