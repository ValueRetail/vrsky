/**
 * WebSocket/SSE Service
 * Handles Server-Sent Events connection for real-time metrics
 * Uses EventSource API (not native WebSocket)
 */

import { config } from '@/config/env'
import type { ConnectionMetricsResponse } from '../types/api'

interface SSEOptions {
  maxRetries?: number
  retryDelayMs?: number
}

class SSEConnection {
  private eventSource: EventSource | null = null
  private connectionId: string | null = null
  private retryCount = 0
  private maxRetries: number
  private retryDelayMs: number
  private isConnecting = false
  private onMessageCallback: ((data: ConnectionMetricsResponse) => void) | null = null
  private onErrorCallback: ((error: Error) => void) | null = null

  constructor(options: SSEOptions = {}) {
    this.maxRetries = options.maxRetries ?? 5
    this.retryDelayMs = options.retryDelayMs ?? 1000
  }

  /**
   * Connect to SSE stream for a connection
   */
  connect(
    connectionId: string,
    onMessage: (data: ConnectionMetricsResponse) => void,
    onError?: (error: Error) => void
  ): void {
    if (this.isConnecting || this.eventSource) {
      return
    }

    this.connectionId = connectionId
    this.onMessageCallback = onMessage
    this.onErrorCallback = onError || null
    this.retryCount = 0

    this._connect()
  }

  /**
   * Internal connect method with retry logic
   */
  private _connect(): void {
    if (!this.connectionId) return

    this.isConnecting = true
    const url = `${config.wsUrl}/api/v1/connections/${this.connectionId}/metrics/stream`

    try {
      this.eventSource = new EventSource(url)

      this.eventSource.addEventListener('metrics', (event) => {
        try {
          const data = JSON.parse(event.data) as ConnectionMetricsResponse
          this.retryCount = 0 // Reset retry count on success
          if (this.onMessageCallback) {
            this.onMessageCallback(data)
          }
        } catch (err) {
          const error = err instanceof Error ? err : new Error(String(err))
          if (this.onErrorCallback) {
            this.onErrorCallback(error)
          }
        }
      })

      this.eventSource.addEventListener('error', () => {
        this._handleError()
      })

      this.isConnecting = false
    } catch (err) {
      const error = err instanceof Error ? err : new Error(String(err))
      this.isConnecting = false
      this._handleError(error)
    }
  }

  /**
   * Handle connection errors and retry
   */
  private _handleError(error?: Error): void {
    this.eventSource?.close()
    this.eventSource = null

    if (this.retryCount < this.maxRetries) {
      this.retryCount++
      const delay = this.retryDelayMs * Math.pow(2, this.retryCount - 1) // Exponential backoff
      setTimeout(() => this._connect(), delay)
    } else if (this.onErrorCallback) {
      const err = error || new Error('Max retries exceeded')
      this.onErrorCallback(err)
    }
  }

  /**
   * Check if connection is active
   */
  isConnected(): boolean {
    return this.eventSource !== null && this.eventSource.readyState === EventSource.OPEN
  }

  /**
   * Disconnect from SSE stream
   */
  disconnect(): void {
    this.eventSource?.close()
    this.eventSource = null
    this.connectionId = null
    this.onMessageCallback = null
    this.onErrorCallback = null
    this.isConnecting = false
  }
}

// Global SSE instance
let sseConnection: SSEConnection | null = null

/**
 * Get or create SSE connection instance
 */
function getSSEConnection(): SSEConnection {
  if (!sseConnection) {
    sseConnection = new SSEConnection()
  }
  return sseConnection
}

/**
 * Connect to metrics stream
 */
export function connectToMetricsStream(
  connectionId: string,
  onMessage: (data: ConnectionMetricsResponse) => void,
  onError?: (error: Error) => void
): void {
  const sse = getSSEConnection()
  sse.connect(connectionId, onMessage, onError)
}

/**
 * Disconnect from metrics stream
 */
export function disconnectMetricsStream(): void {
  const sse = getSSEConnection()
  sse.disconnect()
}

/**
 * Check if metrics stream is connected
 */
export function isMetricsStreamConnected(): boolean {
  const sse = getSSEConnection()
  return sse.isConnected()
}

export const websocketService = {
  connect: connectToMetricsStream,
  disconnect: disconnectMetricsStream,
  isConnected: isMetricsStreamConnected,
}
