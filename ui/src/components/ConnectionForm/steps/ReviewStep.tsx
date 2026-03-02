import type { ConnectionFormData } from '../../../types/forms'

interface ReviewStepProps {
  formData: ConnectionFormData
}

export default function ReviewStep({ formData }: ReviewStepProps) {
  return (
    <div className="space-y-6">
      <div className="bg-blue-50 border border-blue-200 rounded-lg p-4">
        <p className="text-sm text-blue-900">
          Review your configuration below. Click "Create Connection" to complete setup.
        </p>
      </div>

      {/* Basic Info */}
      <section className="border border-gray-200 rounded-lg p-6">
        <h3 className="text-lg font-semibold text-gray-900 mb-4">Basic Information</h3>
        <div className="space-y-3">
          <div>
            <p className="text-sm text-gray-600">Name</p>
            <p className="text-gray-900 font-medium">{formData.basicInfo.name}</p>
          </div>
          {formData.basicInfo.description && (
            <div>
              <p className="text-sm text-gray-600">Description</p>
              <p className="text-gray-900">{formData.basicInfo.description}</p>
            </div>
          )}
        </div>
      </section>

      {/* Source Config */}
      <section className="border border-gray-200 rounded-lg p-6">
        <h3 className="text-lg font-semibold text-gray-900 mb-4">Source Configuration</h3>
        <div className="bg-gray-50 rounded p-4 overflow-x-auto">
          <pre className="text-sm font-mono text-gray-800">
            {JSON.stringify(formData.source, null, 2)}
          </pre>
        </div>
      </section>

      {/* Converter Config */}
      <section className="border border-gray-200 rounded-lg p-6">
        <h3 className="text-lg font-semibold text-gray-900 mb-4">Converter Configuration</h3>
        <div className="bg-gray-50 rounded p-4 overflow-x-auto">
          <pre className="text-sm font-mono text-gray-800">
            {JSON.stringify(formData.converter, null, 2)}
          </pre>
        </div>
      </section>

      {/* Filter Config */}
      <section className="border border-gray-200 rounded-lg p-6">
        <h3 className="text-lg font-semibold text-gray-900 mb-4">Filter Configuration</h3>
        <div className="bg-gray-50 rounded p-4 overflow-x-auto">
          <pre className="text-sm font-mono text-gray-800">
            {JSON.stringify(formData.filter, null, 2)}
          </pre>
        </div>
      </section>

      {/* Destination Config */}
      <section className="border border-gray-200 rounded-lg p-6">
        <h3 className="text-lg font-semibold text-gray-900 mb-4">Destination Configuration</h3>
        <div className="bg-gray-50 rounded p-4 overflow-x-auto">
          <pre className="text-sm font-mono text-gray-800">
            {JSON.stringify(formData.destination, null, 2)}
          </pre>
        </div>
      </section>
    </div>
  )
}
