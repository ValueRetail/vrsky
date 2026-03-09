import { useState } from 'react'
import type { Node } from 'reactflow'

interface NodeData {
  label: string
  config?: Record<string, unknown>
  type?: string
}

export default function PropertyEditor({
  node,
  onUpdate,
  onClose,
  onDelete,
}: {
  node: Node<NodeData>
  onUpdate: (config: Record<string, unknown>) => void
  onClose: () => void
  onDelete?: () => void
}) {
  const [config, setConfig] = useState(node.data.config || {})

  const renderConfigFields = () => {
    const nodeType = node.type as string

    switch (nodeType) {
      case 'consumer':
        return (
          <div className="space-y-4">
            <div>
              <label className="block text-sm font-semibold mb-2 text-gray-700">Source Type</label>
              <select
                value={(config.type as string) || 'http'}
                onChange={(e) => setConfig({ ...config, type: e.target.value })}
                className="w-full px-3 py-2 border border-gray-300 rounded-md bg-white text-gray-900 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
              >
                <option value="http">HTTP Webhook</option>
                <option value="file">File Watcher</option>
                <option value="database">Database CDC</option>
              </select>
            </div>

            {config.type === 'http' && (
              <>
                <div>
                  <label className="block text-sm font-semibold mb-2 text-gray-700">Webhook URL</label>
                  <input
                    type="text"
                    placeholder="https://example.com/webhook"
                    value={(config.http as any)?.url || ''}
                    onChange={(e) =>
                      setConfig({
                        ...config,
                        http: { ...(config.http as any), url: e.target.value },
                      })
                    }
                    className="w-full px-3 py-2 border border-gray-300 rounded-md bg-white text-gray-900 placeholder-gray-400 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
                  />
                </div>
                <div>
                  <label className="block text-sm font-semibold mb-2 text-gray-700">HTTP Method</label>
                  <select
                    value={(config.http as any)?.method || 'POST'}
                    onChange={(e) =>
                      setConfig({
                        ...config,
                        http: { ...(config.http as any), method: e.target.value },
                      })
                    }
                    className="w-full px-3 py-2 border border-gray-300 rounded-md bg-white text-gray-900 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
                  >
                    <option>POST</option>
                    <option>GET</option>
                    <option>PUT</option>
                  </select>
                </div>
              </>
            )}

            {config.type === 'file' && (
              <div>
                <label className="block text-sm font-semibold mb-2 text-gray-700">Watch Directory</label>
                <input
                  type="text"
                  placeholder="/tmp/input"
                  value={(config.file as any)?.path || ''}
                  onChange={(e) =>
                    setConfig({
                      ...config,
                      file: { ...(config.file as any), path: e.target.value },
                    })
                  }
                  className="w-full px-3 py-2 border border-gray-300 rounded-md bg-white text-gray-900 placeholder-gray-400 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
                />
              </div>
            )}
          </div>
        )

      case 'producer':
        return (
          <div className="space-y-4">
            <div>
              <label className="block text-sm font-semibold mb-2 text-gray-700">Destination Type</label>
              <select
                value={(config.type as string) || 'http'}
                onChange={(e) => setConfig({ ...config, type: e.target.value })}
                className="w-full px-3 py-2 border border-gray-300 rounded-md bg-white text-gray-900 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
              >
                <option value="http">HTTP API</option>
                <option value="file">File Output</option>
                <option value="database">Database</option>
              </select>
            </div>

            {config.type === 'http' && (
              <>
                <div>
                  <label className="block text-sm font-semibold mb-2 text-gray-700">Target URL</label>
                  <input
                    type="text"
                    placeholder="https://example.com/api"
                    value={(config.http as any)?.url || ''}
                    onChange={(e) =>
                      setConfig({
                        ...config,
                        http: { ...(config.http as any), url: e.target.value },
                      })
                    }
                    className="w-full px-3 py-2 border border-gray-300 rounded-md bg-white text-gray-900 placeholder-gray-400 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
                  />
                </div>
                <div>
                  <label className="block text-sm font-semibold mb-2 text-gray-700">HTTP Method</label>
                  <select
                    value={(config.http as any)?.method || 'POST'}
                    onChange={(e) =>
                      setConfig({
                        ...config,
                        http: { ...(config.http as any), method: e.target.value },
                      })
                    }
                    className="w-full px-3 py-2 border border-gray-300 rounded-md bg-white text-gray-900 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
                  >
                    <option>POST</option>
                    <option>PUT</option>
                    <option>PATCH</option>
                  </select>
                </div>
              </>
            )}

            {config.type === 'file' && (
              <div>
                <label className="block text-sm font-semibold mb-2 text-gray-700">Output Directory</label>
                <input
                  type="text"
                  placeholder="/tmp/output"
                  value={(config.file as any)?.path || ''}
                  onChange={(e) =>
                    setConfig({
                      ...config,
                      file: { ...(config.file as any), path: e.target.value },
                    })
                  }
                  className="w-full px-3 py-2 border border-gray-300 rounded-md bg-white text-gray-900 placeholder-gray-400 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
                />
              </div>
            )}
          </div>
        )

      case 'filter':
      case 'converter':
        return (
          <div className="space-y-4">
            <p className="text-sm text-gray-500 italic">Configuration coming soon</p>
          </div>
        )

      default:
        return <p className="text-sm text-gray-500">No configuration available</p>
    }
  }

  return (
    <div className="h-full bg-white flex flex-col">
      {/* Header */}
      <div className="p-4 border-b border-gray-200 flex items-center justify-between bg-gradient-to-r from-blue-50 to-white">
        <h3 className="font-bold text-gray-900 text-lg">{node.data.label}</h3>
        <button
          onClick={onClose}
          className="text-gray-400 hover:text-gray-600 text-2xl transition-colors"
        >
          ✕
        </button>
      </div>

      {/* Config Fields */}
      <div className="flex-1 overflow-y-auto p-4 text-gray-900">{renderConfigFields()}</div>

      {/* Footer Actions */}
      <div className="p-4 border-t border-gray-200 space-y-2 bg-gray-50">
        <button
          onClick={() => {
            onUpdate(config)
            onClose()
          }}
          className="w-full px-3 py-2 bg-blue-500 text-white rounded-md hover:bg-blue-600 active:bg-blue-700 font-medium text-sm transition-colors"
        >
          Save Configuration
        </button>

        {onDelete && (
          <button
            onClick={onDelete}
            className="w-full px-3 py-2 bg-red-500 text-white rounded-md hover:bg-red-600 active:bg-red-700 font-medium text-sm transition-colors"
          >
            Delete Node
          </button>
        )}
      </div>
    </div>
  )
}
