/**
 * Connections Store Tests
 */

import { describe, it, expect, beforeEach } from 'vitest'
import { useConnectionsStore } from '@/store/connectionsStore'
import type { Connection } from '@/types/models'

const mockConnection: Connection = {
  id: 'conn-1',
  tenant_id: 'tenant-1',
  name: 'Test Connection',
  description: 'Test Description',
  status: 'stopped',
  source_config: { type: 'http', url: 'http://example.com', method: 'GET' },
  converter_config: { type: 'schema', input_schema: {} },
  filter_config: { type: 'rules', rules: [] },
  destination_config: { type: 'http', url: 'http://example.com', method: 'POST' },
  created_at: '2024-01-01T00:00:00Z',
  updated_at: '2024-01-01T00:00:00Z',
}

describe('Connections Store', () => {
  beforeEach(() => {
    useConnectionsStore.setState({
      connections: [],
      selectedConnection: null,
      loading: false,
      error: null,
    })
  })

  describe('Connections CRUD', () => {
    it('should add a connection', () => {
      const store = useConnectionsStore.getState()
      store.addConnection(mockConnection)

      const state = useConnectionsStore.getState()
      expect(state.connections).toHaveLength(1)
      expect(state.connections[0]).toEqual(mockConnection)
    })

    it('should update a connection', () => {
      const store = useConnectionsStore.getState()
      store.addConnection(mockConnection)

      const updated = { ...mockConnection, status: 'running' as const }
      store.updateConnection(updated)

      const state = useConnectionsStore.getState()
      expect(state.connections[0].status).toBe('running')
    })

    it('should delete a connection', () => {
      const store = useConnectionsStore.getState()
      store.addConnection(mockConnection)
      expect(store.getConnectionById('conn-1')).toEqual(mockConnection)

      store.deleteConnection('conn-1')
      const state = useConnectionsStore.getState()
      expect(state.connections).toHaveLength(0)
    })

    it('should set multiple connections', () => {
      const store = useConnectionsStore.getState()
      const connections = [mockConnection, { ...mockConnection, id: 'conn-2' }]
      store.setConnections(connections)

      const state = useConnectionsStore.getState()
      expect(state.connections).toHaveLength(2)
    })

    it('should clear all connections', () => {
      const store = useConnectionsStore.getState()
      store.addConnection(mockConnection)
      store.clear()

      const state = useConnectionsStore.getState()
      expect(state.connections).toHaveLength(0)
      expect(state.selectedConnection).toBeNull()
      expect(state.error).toBeNull()
    })
  })

  describe('Getters', () => {
    it('should get connection by ID', () => {
      const store = useConnectionsStore.getState()
      store.addConnection(mockConnection)

      const connection = store.getConnectionById('conn-1')
      expect(connection).toEqual(mockConnection)
    })

    it('should return undefined for non-existent connection', () => {
      const store = useConnectionsStore.getState()
      const connection = store.getConnectionById('non-existent')
      expect(connection).toBeUndefined()
    })

    it('should get running connections', () => {
      const store = useConnectionsStore.getState()
      store.addConnection(mockConnection)
      store.addConnection({
        ...mockConnection,
        id: 'conn-2',
        status: 'running',
      })

      const running = store.getRunningConnections()
      expect(running).toHaveLength(1)
      expect(running[0].id).toBe('conn-2')
    })

    it('should get stopped connections', () => {
      const store = useConnectionsStore.getState()
      store.addConnection(mockConnection)
      store.addConnection({
        ...mockConnection,
        id: 'conn-2',
        status: 'running',
      })

      const stopped = store.getStoppedConnections()
      expect(stopped).toHaveLength(1)
      expect(stopped[0].id).toBe('conn-1')
    })

    it('should get error connections', () => {
      const store = useConnectionsStore.getState()
      store.addConnection(mockConnection)
      store.addConnection({
        ...mockConnection,
        id: 'conn-2',
        status: 'error',
      })

      const errors = store.getErrorConnections()
      expect(errors).toHaveLength(1)
      expect(errors[0].id).toBe('conn-2')
    })
  })

  describe('Loading State', () => {
    it('should set loading state', () => {
      const store = useConnectionsStore.getState()
      store.setLoading(true)

      let state = useConnectionsStore.getState()
      expect(state.loading).toBe(true)

      store.setLoading(false)
      state = useConnectionsStore.getState()
      expect(state.loading).toBe(false)
    })
  })

  describe('Error State', () => {
    it('should set error state', () => {
      const store = useConnectionsStore.getState()
      store.setError('Connection failed')

      let state = useConnectionsStore.getState()
      expect(state.error).toBe('Connection failed')

      store.setError(null)
      state = useConnectionsStore.getState()
      expect(state.error).toBeNull()
    })
  })

  describe('Selected Connection', () => {
    it('should set selected connection', () => {
      const store = useConnectionsStore.getState()
      store.setSelectedConnection(mockConnection)

      const state = useConnectionsStore.getState()
      expect(state.selectedConnection).toEqual(mockConnection)
    })

    it('should clear selected connection', () => {
      const store = useConnectionsStore.getState()
      store.setSelectedConnection(mockConnection)
      store.setSelectedConnection(null)

      const state = useConnectionsStore.getState()
      expect(state.selectedConnection).toBeNull()
    })

    it('should clear selected connection when it is deleted', () => {
      const store = useConnectionsStore.getState()
      store.addConnection(mockConnection)
      store.setSelectedConnection(mockConnection)

      store.deleteConnection('conn-1')

      const state = useConnectionsStore.getState()
      expect(state.selectedConnection).toBeNull()
    })
  })
})
