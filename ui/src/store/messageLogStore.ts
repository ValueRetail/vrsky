/**
 * Message Log Store
 * Manages test message logs for connections
 */

import { create } from 'zustand'
import type { MessageLogEntry } from '../components/TestData/MessageLog'

interface MessageLogStore {
  logs: Map<string, MessageLogEntry[]>

  // Actions
  addMessage: (connectionId: string, message: MessageLogEntry) => void
  addMessages: (connectionId: string, messages: MessageLogEntry[]) => void
  getMessages: (connectionId: string) => MessageLogEntry[]
  clearMessages: (connectionId: string) => void
  clearAllMessages: () => void

  // Helpers
  getRecentMessages: (connectionId: string, limit: number) => MessageLogEntry[]
}

export const useMessageLogStore = create<MessageLogStore>((set, get) => ({
  logs: new Map(),

  addMessage: (connectionId, message) => {
    set((state) => {
      const newLogs = new Map(state.logs)
      const connectionLogs = newLogs.get(connectionId) || []
      // Keep only last 1000 messages
      const updatedLogs = [message, ...connectionLogs].slice(0, 1000)
      newLogs.set(connectionId, updatedLogs)
      return { logs: newLogs }
    })
  },

  addMessages: (connectionId, messages) => {
    set((state) => {
      const newLogs = new Map(state.logs)
      const connectionLogs = newLogs.get(connectionId) || []
      const updatedLogs = [...messages, ...connectionLogs].slice(0, 1000)
      newLogs.set(connectionId, updatedLogs)
      return { logs: newLogs }
    })
  },

  getMessages: (connectionId) => {
    const { logs } = get()
    return logs.get(connectionId) || []
  },

  clearMessages: (connectionId) => {
    set((state) => {
      const newLogs = new Map(state.logs)
      newLogs.delete(connectionId)
      return { logs: newLogs }
    })
  },

  clearAllMessages: () => set({ logs: new Map() }),

  getRecentMessages: (connectionId, limit) => {
    const { getMessages } = get()
    return getMessages(connectionId).slice(0, limit)
  },
}))
