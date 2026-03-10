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
    <div className="fixed inset-0 bg-black/50 dark:bg-black/60 backdrop-blur-sm flex items-center justify-center z-50 animate-fade-in">
      <div className="card-elevated max-w-md w-full mx-4 p-8 space-y-6 animate-slide-in-up">
        {/* Icon and Title */}
        <div className="flex items-start gap-4">
          <div className={`p-3 rounded-lg flex-shrink-0 text-2xl ${
            config.destructive
              ? 'bg-danger-100 dark:bg-danger-900/30'
              : 'bg-primary-100 dark:bg-primary-900/30'
          }`}>
            {config.destructive ? '⚠' : 'ℹ'}
          </div>

          <div className="flex-1">
            <h2 className="text-lg font-bold text-neutral-900 dark:text-neutral-50">
              {config.title}
            </h2>
            <p className="text-sm text-neutral-600 dark:text-neutral-400 mt-1">
              {config.message}
            </p>
          </div>
        </div>

        {/* Actions */}
        <div className="flex gap-3 justify-end pt-4 border-t border-neutral-200 dark:border-neutral-700">
          <button
            onClick={handleCancel}
            className="btn-secondary"
          >
            {config.cancelLabel || 'Cancel'}
          </button>
          <button
            onClick={handleConfirm}
            className={config.destructive ? 'btn-danger' : 'btn-primary'}
          >
            {config.confirmLabel || 'Confirm'}
          </button>
        </div>
      </div>
    </div>
  )
}
