/**
 * Message Log Store Tests
 */

import { describe, it, expect, beforeEach } from 'vitest'
import { useMessageLogStore } from '@/store/messageLogStore'
import type { MessageLogEntry } from '@/components/TestData/MessageLog'

const mockMessage: MessageLogEntry = {
  id: 'msg-1',
  timestamp: new Date().toISOString(),
  status: 'sent',
  message: '{"test": "message"}',
}

describe('Message Log Store', () => {
  beforeEach(() => {
    useMessageLogStore.setState({
      logs: new Map(),
    })
  })

  describe('Message Operations', () => {
    it('should add a message', () => {
      const store = useMessageLogStore.getState()
      store.addMessage('conn-1', mockMessage)

      const state = useMessageLogStore.getState()
      expect(state.logs.size).toBe(1)
      expect(state.logs.get('conn-1')).toContain(mockMessage)
    })

    it('should add multiple messages', () => {
      const store = useMessageLogStore.getState()
      const messages = [mockMessage, { ...mockMessage, id: 'msg-2' }, { ...mockMessage, id: 'msg-3' }]

      messages.forEach((msg) => store.addMessage('conn-1', msg))

      const state = useMessageLogStore.getState()
      expect(state.logs.get('conn-1')).toHaveLength(3)
    })

    it('should add messages in reverse order (newest first)', () => {
      const store = useMessageLogStore.getState()
      const msg1 = { ...mockMessage, id: 'msg-1' }
      const msg2 = { ...mockMessage, id: 'msg-2' }

      store.addMessage('conn-1', msg1)
      store.addMessage('conn-1', msg2)

      const messages = store.getMessages('conn-1')
      expect(messages[0]?.id).toBe('msg-2')
      expect(messages[1]?.id).toBe('msg-1')
    })

    it('should add multiple messages at once', () => {
      const store = useMessageLogStore.getState()
      const messages = [
        { ...mockMessage, id: 'msg-1' },
        { ...mockMessage, id: 'msg-2' },
      ]

      store.addMessages('conn-1', messages)

      const state = useMessageLogStore.getState()
      expect(state.logs.get('conn-1')).toHaveLength(2)
    })

    it('should limit messages to 1000 per connection', () => {
      const store = useMessageLogStore.getState()

      for (let i = 0; i < 1100; i++) {
        store.addMessage('conn-1', {
          ...mockMessage,
          id: `msg-${i}`,
        })
      }

      const messages = store.getMessages('conn-1')
      expect(messages).toHaveLength(1000)
    })

    it('should clear messages for a connection', () => {
      const store = useMessageLogStore.getState()
      store.addMessage('conn-1', mockMessage)
      store.addMessage('conn-1', { ...mockMessage, id: 'msg-2' })

      store.clearMessages('conn-1')

      const state = useMessageLogStore.getState()
      expect(state.logs.get('conn-1')).toBeUndefined()
    })

    it('should clear all messages', () => {
      const store = useMessageLogStore.getState()
      store.addMessage('conn-1', mockMessage)
      store.addMessage('conn-2', { ...mockMessage, id: 'msg-2' })

      store.clearAllMessages()

      const state = useMessageLogStore.getState()
      expect(state.logs.size).toBe(0)
    })
  })

  describe('Getters', () => {
    it('should get messages for a connection', () => {
      const store = useMessageLogStore.getState()
      const msg1 = { ...mockMessage, id: 'msg-1' }
      const msg2 = { ...mockMessage, id: 'msg-2' }

      store.addMessage('conn-1', msg1)
      store.addMessage('conn-1', msg2)

      const messages = store.getMessages('conn-1')
      expect(messages).toHaveLength(2)
    })

    it('should return empty array for non-existent connection', () => {
      const store = useMessageLogStore.getState()
      const messages = store.getMessages('non-existent')
      expect(messages).toHaveLength(0)
    })

    it('should get recent messages with limit', () => {
      const store = useMessageLogStore.getState()

      for (let i = 0; i < 10; i++) {
        store.addMessage('conn-1', {
          ...mockMessage,
          id: `msg-${i}`,
        })
      }

      const recent = store.getRecentMessages('conn-1', 3)
      expect(recent).toHaveLength(3)
    })

    it('should respect limit for recent messages', () => {
      const store = useMessageLogStore.getState()

      for (let i = 0; i < 5; i++) {
        store.addMessage('conn-1', {
          ...mockMessage,
          id: `msg-${i}`,
        })
      }

      const recent = store.getRecentMessages('conn-1', 10)
      expect(recent).toHaveLength(5)
    })
  })

  describe('Multiple Connections', () => {
    it('should handle messages for multiple connections independently', () => {
      const store = useMessageLogStore.getState()

      store.addMessage('conn-1', { ...mockMessage, id: 'msg-1' })
      store.addMessage('conn-2', { ...mockMessage, id: 'msg-2' })

      expect(store.getMessages('conn-1')).toHaveLength(1)
      expect(store.getMessages('conn-2')).toHaveLength(1)
    })

    it('should clear only specified connection messages', () => {
      const store = useMessageLogStore.getState()

      store.addMessage('conn-1', { ...mockMessage, id: 'msg-1' })
      store.addMessage('conn-2', { ...mockMessage, id: 'msg-2' })

      store.clearMessages('conn-1')

      const state = useMessageLogStore.getState()
      expect(state.logs.get('conn-1')).toBeUndefined()
      expect(state.logs.get('conn-2')).toHaveLength(1)
    })
  })
})
