/**
 * Confirm Dialog
 * Reusable confirmation modal
 */

import type { ConfirmDialogConfig } from '../../types/models'

interface ConfirmDialogProps {
  config: ConfirmDialogConfig | null
  onClose: () => void
}

export default function ConfirmDialog({ config, onClose }: ConfirmDialogProps) {
  if (!config) return null

  const handleConfirm = async () => {
    await config.onConfirm()
    onClose()
  }

  const handleCancel = () => {
    config.onCancel?.()
    onClose()
  }

  return (
    <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
      <div className="bg-white rounded-lg shadow-lg max-w-md w-full mx-4 p-6">
        <h2 className="text-lg font-bold text-gray-900 mb-2">{config.title}</h2>
        <p className="text-gray-600 mb-6">{config.message}</p>

        <div className="flex gap-3 justify-end">
          <button
            onClick={handleCancel}
            className="px-4 py-2 text-gray-700 bg-gray-100 rounded-lg hover:bg-gray-200 transition-colors font-medium"
          >
            {config.cancelLabel || 'Cancel'}
          </button>
          <button
            onClick={handleConfirm}
            className={`px-4 py-2 text-white rounded-lg transition-colors font-medium ${
              config.destructive
                ? 'bg-red-500 hover:bg-red-600'
                : 'bg-blue-500 hover:bg-blue-600'
            }`}
          >
            {config.confirmLabel || 'Confirm'}
          </button>
        </div>
      </div>
    </div>
  )
}
