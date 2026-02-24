/**
 * UI Store
 * Manages UI state (notifications, dialogs, sidebar, etc.)
 */

import { create } from 'zustand'
import type { Notification, ConfirmDialogConfig } from '../types/models'

interface UIStore {
  sidebarOpen: boolean
  notifications: Notification[]
  confirmDialog: ConfirmDialogConfig | null

  // Sidebar Actions
  toggleSidebar: () => void
  setSidebarOpen: (open: boolean) => void

  // Notification Actions
  addNotification: (notification: Omit<Notification, 'id'>) => string
  removeNotification: (id: string) => void
  clearNotifications: () => void

  // Confirm Dialog Actions
  showConfirmDialog: (config: ConfirmDialogConfig) => void
  hideConfirmDialog: () => void

  // Helpers
  getNotificationCount: () => number
  showSuccessNotification: (title: string, message: string, duration?: number) => string
  showErrorNotification: (title: string, message: string, duration?: number) => string
  showWarningNotification: (title: string, message: string, duration?: number) => string
  showInfoNotification: (title: string, message: string, duration?: number) => string
}

export const useUIStore = create<UIStore>((set, get) => ({
  sidebarOpen: true,
  notifications: [],
  confirmDialog: null,

  toggleSidebar: () => {
    set((state) => ({ sidebarOpen: !state.sidebarOpen }))
  },

  setSidebarOpen: (open) => set({ sidebarOpen: open }),

  addNotification: (notification) => {
    const id = `notification-${Date.now()}-${Math.random()}`
    const newNotification: Notification = {
      ...notification,
      id,
      duration: notification.duration ?? 5000,
    }

    set((state) => ({
      notifications: [...state.notifications, newNotification],
    }))

    // Auto-remove notification after duration
    if (newNotification.duration && newNotification.duration > 0) {
      setTimeout(() => {
        get().removeNotification(id)
      }, newNotification.duration)
    }

    return id
  },

  removeNotification: (id) => {
    set((state) => ({
      notifications: state.notifications.filter((n) => n.id !== id),
    }))
  },

  clearNotifications: () => set({ notifications: [] }),

  showConfirmDialog: (config) => set({ confirmDialog: config }),

  hideConfirmDialog: () => set({ confirmDialog: null }),

  getNotificationCount: () => {
    const { notifications } = get()
    return notifications.length
  },

  showSuccessNotification: (title, message, duration) => {
    return get().addNotification({
      type: 'success',
      title,
      message,
      duration,
    })
  },

  showErrorNotification: (title, message, duration) => {
    return get().addNotification({
      type: 'error',
      title,
      message,
      duration,
    })
  },

  showWarningNotification: (title, message, duration) => {
    return get().addNotification({
      type: 'warning',
      title,
      message,
      duration,
    })
  },

  showInfoNotification: (title, message, duration) => {
    return get().addNotification({
      type: 'info',
      title,
      message,
      duration,
    })
  },
}))
