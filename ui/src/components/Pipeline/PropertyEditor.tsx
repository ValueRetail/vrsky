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
}: {
  node: Node<NodeData>
  onUpdate: (config: Record<string, unknown>) => void
  onClose: () => void
}) {
  const [config, setConfig] = useState(node.data.config || {})

  const renderConfigFields = () => {
    const nodeType = node.type as string

    switch (nodeType) {
      case 'consumer':
        return (
          <div className="space-y-4">
            <div>
              <label className="block text-sm font-semibold mb-2 text-slate-100">Source Type</label>
              <select
                value={(config.type as string) || 'http'}
                onChange={(e) => setConfig({ ...config, type: e.target.value })}
                className="w-full px-3 py-2 border border-slate-600 rounded-md bg-slate-800 text-slate-100 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
              >
                <option value="http">HTTP Webhook</option>
                <option value="file">File Watcher</option>
                <option value="database">Database CDC</option>
              </select>
            </div>

            {config.type === 'http' && (
              <>
                <div>
                  <label className="block text-sm font-semibold mb-2 text-slate-100">Webhook URL</label>
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
                    className="w-full px-3 py-2 border border-slate-600 rounded-md bg-slate-800 text-slate-100 placeholder-slate-500 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
                  />
                </div>
                <div>
                  <label className="block text-sm font-semibold mb-2 text-slate-100">HTTP Method</label>
                  <select
                    value={(config.http as any)?.method || 'POST'}
                    onChange={(e) =>
                      setConfig({
                        ...config,
                        http: { ...(config.http as any), method: e.target.value },
                      })
                    }
                    className="w-full px-3 py-2 border border-slate-600 rounded-md bg-slate-800 text-slate-100 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
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
                <label className="block text-sm font-semibold mb-2 text-slate-100">Watch Directory</label>
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
                  className="w-full px-3 py-2 border border-slate-600 rounded-md bg-slate-800 text-slate-100 placeholder-slate-500 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
                />
              </div>
            )}
          </div>
        )

      case 'producer':
        return (
          <div className="space-y-4">
            <div>
              <label className="block text-sm font-semibold mb-2 text-slate-100">Destination Type</label>
              <select
                value={(config.type as string) || 'http'}
                onChange={(e) => setConfig({ ...config, type: e.target.value })}
                className="w-full px-3 py-2 border border-slate-600 rounded-md bg-slate-800 text-slate-100 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
              >
                <option value="http">HTTP API</option>
                <option value="file">File Output</option>
                <option value="database">Database</option>
              </select>
            </div>

            {config.type === 'http' && (
              <>
                <div>
                  <label className="block text-sm font-semibold mb-2 text-slate-100">Target URL</label>
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
                    className="w-full px-3 py-2 border border-slate-600 rounded-md bg-slate-800 text-slate-100 placeholder-slate-500 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
                  />
                </div>
                <div>
                  <label className="block text-sm font-semibold mb-2 text-slate-100">HTTP Method</label>
                  <select
                    value={(config.http as any)?.method || 'POST'}
                    onChange={(e) =>
                      setConfig({
                        ...config,
                        http: { ...(config.http as any), method: e.target.value },
                      })
                    }
                    className="w-full px-3 py-2 border border-slate-600 rounded-md bg-slate-800 text-slate-100 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
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
                <label className="block text-sm font-semibold mb-2 text-slate-100">Output Directory</label>
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
                  className="w-full px-3 py-2 border border-slate-600 rounded-md bg-slate-800 text-slate-100 placeholder-slate-500 focus:border-blue-400 focus:ring-1 focus:ring-blue-400 outline-none"
                />
              </div>
            )}
          </div>
        )

      case 'filter':
      case 'converter':
        return (
          <div className="space-y-4">
            <p className="text-sm text-slate-400 italic">Configuration coming soon</p>
          </div>
        )

      default:
        return <p className="text-sm text-slate-500">No configuration available</p>
    }
  }

  return (
    <div className="w-96 bg-slate-900 border-l border-slate-700 shadow-2xl flex flex-col">
      {/* Header */}
      <div className="p-4 border-b border-slate-700 flex items-center justify-between bg-slate-950">
        <h3 className="font-bold text-white text-lg">{node.data.label}</h3>
        <button
          onClick={onClose}
          className="text-slate-400 hover:text-slate-200 text-xl transition-colors"
        >
          ✕
        </button>
      </div>

      {/* Config Fields */}
      <div className="flex-1 p-4 overflow-auto text-slate-100">
        {renderConfigFields()}
      </div>

      {/* Footer */}
      <div className="p-4 border-t border-slate-700 flex gap-2 bg-slate-950">
        <button
          onClick={() => {
            onUpdate(config)
            onClose()
          }}
          className="flex-1 px-3 py-2 bg-blue-600 text-white rounded-md hover:bg-blue-700 active:bg-blue-800 font-medium text-sm transition-colors"
        >
          Save
        </button>
        <button
          onClick={onClose}
          className="flex-1 px-3 py-2 bg-slate-700 text-slate-100 rounded-md hover:bg-slate-600 active:bg-slate-500 font-medium text-sm transition-colors"
        >
          Cancel
        </button>
      </div>
    </div>
  )
}
