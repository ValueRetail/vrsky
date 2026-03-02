/**
 * UI Store Tests
 */

import { describe, it, expect, beforeEach } from 'vitest'
import { useUIStore } from '@/store/uiStore'

describe('UI Store', () => {
  beforeEach(() => {
    useUIStore.setState({
      notifications: [],
      confirmDialog: null,
      toastTimeout: null,
    })
  })

  describe('Notifications', () => {
    it('should add a notification', () => {
      const store = useUIStore.getState()
      store.addNotification({
        type: 'success',
        title: 'Test',
        message: 'Test message',
      })

      const state = useUIStore.getState()
      expect(state.notifications).toHaveLength(1)
      expect(state.notifications[0]?.title).toBe('Test')
      expect(state.notifications[0]?.type).toBe('success')
    })

    it('should add multiple notifications', () => {
      const store = useUIStore.getState()
      store.addNotification({
        type: 'success',
        title: 'Success',
        message: 'Test',
      })
      store.addNotification({
        type: 'error',
        title: 'Error',
        message: 'Test',
      })

      const state = useUIStore.getState()
      expect(state.notifications).toHaveLength(2)
    })

    it('should remove a notification by ID', () => {
      const store = useUIStore.getState()
      store.addNotification({
        type: 'info',
        title: 'Info',
        message: 'Test',
      })

      const state = useUIStore.getState()
      const notificationId = state.notifications[0]?.id
      expect(notificationId).toBeDefined()

      store.removeNotification(notificationId!)
      const updatedState = useUIStore.getState()
      expect(updatedState.notifications).toHaveLength(0)
    })

    it('should clear all notifications', () => {
      const store = useUIStore.getState()
      store.addNotification({
        type: 'success',
        title: 'Test 1',
        message: 'Test',
      })
      store.addNotification({
        type: 'error',
        title: 'Test 2',
        message: 'Test',
      })

      store.clearNotifications()
      const state = useUIStore.getState()
      expect(state.notifications).toHaveLength(0)
    })
  })

  describe('Confirm Dialog', () => {
    it('should show confirm dialog', () => {
      const store = useUIStore.getState()
      const handler = () => {}

      store.showConfirmDialog({
        title: 'Confirm',
        message: 'Are you sure?',
        onConfirm: handler,
      })

      const state = useUIStore.getState()
      expect(state.confirmDialog).not.toBeNull()
      expect(state.confirmDialog?.title).toBe('Confirm')
    })

    it('should hide confirm dialog', () => {
      const store = useUIStore.getState()
      store.showConfirmDialog({
        title: 'Confirm',
        message: 'Are you sure?',
        onConfirm: () => {},
      })

      store.hideConfirmDialog()
      const state = useUIStore.getState()
      expect(state.confirmDialog).toBeNull()
    })

    it('should accept custom dialog options', () => {
      const store = useUIStore.getState()
      store.showConfirmDialog({
        title: 'Delete',
        message: 'Confirm deletion',
        confirmLabel: 'Delete',
        cancelLabel: 'Cancel',
        destructive: true,
        onConfirm: () => {},
      })

      const state = useUIStore.getState()
      expect(state.confirmDialog?.confirmLabel).toBe('Delete')
      expect(state.confirmDialog?.cancelLabel).toBe('Cancel')
      expect(state.confirmDialog?.destructive).toBe(true)
    })
  })
})
