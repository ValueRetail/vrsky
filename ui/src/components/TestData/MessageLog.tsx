/**
 * MessageLog Component
 * Display recent test messages with pagination and details modal
 */

import { useState } from 'react'

export interface MessageLogEntry {
  id: string
  timestamp: string
  status: 'sent' | 'processed' | 'error'
  message: string
  result?: string
  error?: string
}

interface MessageLogProps {
  messages: MessageLogEntry[]
  pageSize?: number
}

export function MessageLog({ messages, pageSize = 10 }: MessageLogProps) {
  const [currentPage, setCurrentPage] = useState(1)
  const [selectedMessage, setSelectedMessage] = useState<MessageLogEntry | null>(null)

  const totalPages = Math.ceil(messages.length / pageSize)
  const startIndex = (currentPage - 1) * pageSize
  const endIndex = startIndex + pageSize
  const currentMessages = messages.slice(startIndex, endIndex)

  const statusColors: Record<string, string> = {
    sent: 'bg-blue-50 text-blue-800 border-blue-200',
    processed: 'bg-green-50 text-green-800 border-green-200',
    error: 'bg-red-50 text-red-800 border-red-200',
  }

  const statusIcons: Record<string, string> = {
    sent: '📤',
    processed: '✅',
    error: '❌',
  }

  if (messages.length === 0) {
    return (
      <div className="p-4 rounded-lg border border-gray-200 bg-gray-50 text-center">
        <p className="text-gray-600 text-sm">No messages yet. Send a test message or start the auto-generator.</p>
      </div>
    )
  }

  return (
    <>
      {/* Message List */}
      <div className="p-4 rounded-lg border border-gray-200 bg-white">
        <div className="flex items-center justify-between mb-3">
          <h3 className="text-sm font-bold text-gray-900">Message Log</h3>
          <span className="text-xs text-gray-600">{messages.length} total messages</span>
        </div>

        <div className="space-y-2">
          {currentMessages.map((msg) => (
            <button
              key={msg.id}
              onClick={() => setSelectedMessage(msg)}
              className={`w-full p-3 rounded border text-left transition-colors hover:bg-gray-50 ${statusColors[msg.status]}`}
            >
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2 flex-1 min-w-0">
                  <span className="text-lg">{statusIcons[msg.status]}</span>
                  <div className="min-w-0 flex-1">
                    <p className="text-xs font-mono truncate max-w-xs">{msg.message}</p>
                  </div>
                </div>
                <span className="text-xs whitespace-nowrap ml-2">
                  {new Date(msg.timestamp).toLocaleTimeString()}
                </span>
              </div>
            </button>
          ))}
        </div>

        {/* Pagination */}
        {totalPages > 1 && (
          <div className="flex items-center justify-between mt-4 pt-4 border-t border-gray-200">
            <button
              onClick={() => setCurrentPage(Math.max(1, currentPage - 1))}
              disabled={currentPage === 1}
              className="px-3 py-1 text-xs font-medium text-gray-700 bg-gray-100 rounded hover:bg-gray-200 disabled:bg-gray-50 disabled:text-gray-400"
            >
              Previous
            </button>
            <span className="text-xs text-gray-600">
              Page {currentPage} of {totalPages}
            </span>
            <button
              onClick={() => setCurrentPage(Math.min(totalPages, currentPage + 1))}
              disabled={currentPage === totalPages}
              className="px-3 py-1 text-xs font-medium text-gray-700 bg-gray-100 rounded hover:bg-gray-200 disabled:bg-gray-50 disabled:text-gray-400"
            >
              Next
            </button>
          </div>
        )}
      </div>

      {/* Message Details Modal */}
      {selectedMessage && (
        <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50 p-4">
          <div className="bg-white rounded-lg shadow-xl max-w-2xl w-full max-h-[80vh] overflow-y-auto">
            {/* Header */}
            <div className="sticky top-0 bg-white border-b border-gray-200 p-4 flex items-center justify-between">
              <h2 className="text-lg font-bold text-gray-900">Message Details</h2>
              <button
                onClick={() => setSelectedMessage(null)}
                className="text-gray-500 hover:text-gray-700 text-2xl leading-none"
              >
                ×
              </button>
            </div>

            {/* Content */}
            <div className="p-4 space-y-4">
              {/* Metadata */}
              <div>
                <h3 className="text-xs font-bold text-gray-700 mb-2 uppercase tracking-wider">Metadata</h3>
                <div className="grid grid-cols-2 gap-3 text-xs">
                  <div>
                    <p className="text-gray-600">ID</p>
                    <p className="font-mono text-gray-900">{selectedMessage.id}</p>
                  </div>
                  <div>
                    <p className="text-gray-600">Status</p>
                    <p className="font-medium capitalize">{selectedMessage.status}</p>
                  </div>
                  <div className="col-span-2">
                    <p className="text-gray-600">Timestamp</p>
                    <p className="font-mono text-gray-900">
                      {new Date(selectedMessage.timestamp).toLocaleString()}
                    </p>
                  </div>
                </div>
              </div>

              {/* Message Content */}
              <div>
                <h3 className="text-xs font-bold text-gray-700 mb-2 uppercase tracking-wider">Message</h3>
                <div className="bg-gray-50 p-3 rounded border border-gray-200 overflow-x-auto">
                  <pre className="text-xs font-mono text-gray-800 whitespace-pre-wrap break-words">
                    {typeof selectedMessage.message === 'string'
                      ? selectedMessage.message
                      : JSON.stringify(selectedMessage.message, null, 2)}
                  </pre>
                </div>
              </div>

              {/* Result (if available) */}
              {selectedMessage.result && (
                <div>
                  <h3 className="text-xs font-bold text-gray-700 mb-2 uppercase tracking-wider">Result</h3>
                  <div className="bg-green-50 p-3 rounded border border-green-200 overflow-x-auto">
                    <pre className="text-xs font-mono text-green-800 whitespace-pre-wrap break-words">
                      {selectedMessage.result}
                    </pre>
                  </div>
                </div>
              )}

              {/* Error (if available) */}
              {selectedMessage.error && (
                <div>
                  <h3 className="text-xs font-bold text-gray-700 mb-2 uppercase tracking-wider">Error</h3>
                  <div className="bg-red-50 p-3 rounded border border-red-200 overflow-x-auto">
                    <pre className="text-xs font-mono text-red-800 whitespace-pre-wrap break-words">
                      {selectedMessage.error}
                    </pre>
                  </div>
                </div>
              )}
            </div>

            {/* Footer */}
            <div className="sticky bottom-0 bg-gray-50 border-t border-gray-200 p-4">
              <button
                onClick={() => setSelectedMessage(null)}
                className="w-full px-4 py-2 bg-blue-600 text-white rounded-md hover:bg-blue-700 font-medium text-sm"
              >
                Close
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}
