/**
 * Toast Notifications
 * Display notifications in top-right corner
 */

import { useUIStore } from '../../store/uiStore'
import type { Notification } from '../../types/models'

function NotificationItem({ notification, onClose }: { notification: Notification; onClose: () => void }) {
  const bgColors: Record<Notification['type'], string> = {
    success: 'bg-success-50 dark:bg-success-900/20 border-success-200 dark:border-success-700/50',
    error: 'bg-danger-50 dark:bg-danger-900/20 border-danger-200 dark:border-danger-700/50',
    warning: 'bg-warning-50 dark:bg-warning-900/20 border-warning-200 dark:border-warning-700/50',
    info: 'bg-primary-50 dark:bg-primary-900/20 border-primary-200 dark:border-primary-700/50',
  }

  const textColors: Record<Notification['type'], string> = {
    success: 'text-success-800 dark:text-success-300',
    error: 'text-danger-800 dark:text-danger-300',
    warning: 'text-warning-800 dark:text-warning-300',
    info: 'text-primary-800 dark:text-primary-300',
  }

  const iconColors: Record<Notification['type'], string> = {
    success: 'text-success-600 dark:text-success-400',
    error: 'text-danger-600 dark:text-danger-400',
    warning: 'text-warning-600 dark:text-warning-400',
    info: 'text-primary-600 dark:text-primary-400',
  }

  return (
    <div className={`border rounded-xl p-4 flex items-start gap-3 shadow-lg backdrop-blur-sm animate-slide-in-up transition-all duration-base ${bgColors[notification.type]} ${textColors[notification.type]}`}>
      <div className={`flex-shrink-0 mt-0.5 text-lg ${iconColors[notification.type]}`}>
        {notification.type === 'success' && '✓'}
        {notification.type === 'error' && '✕'}
        {notification.type === 'warning' && '⚠'}
        {notification.type === 'info' && 'ℹ'}
      </div>

      <div className="flex-1 min-w-0">
        <h3 className="font-semibold text-sm">{notification.title}</h3>
        <p className="text-sm opacity-90 mt-0.5">{notification.message}</p>
      </div>

      <button
        onClick={onClose}
        className="flex-shrink-0 p-1 hover:opacity-70 transition-opacity duration-fast text-lg"
        aria-label="Dismiss notification"
      >
        ✕
      </button>
    </div>
  )
}

export default function Toast() {
  const { notifications, removeNotification } = useUIStore()

  return (
    <div className="fixed top-4 right-4 z-40 space-y-2 max-w-md">
      {notifications.map((notification) => (
        <NotificationItem
          key={notification.id}
          notification={notification}
          onClose={() => removeNotification(notification.id)}
        />
      ))}
    </div>
  )
}
