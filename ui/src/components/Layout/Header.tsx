/**
 * Header Component
 * Top navigation bar
 */

import { useUIStore } from '../../store/uiStore'
import { useConnectionsStore } from '../../store/connectionsStore'

export default function Header() {
  const { toggleSidebar } = useUIStore()
  const { connections } = useConnectionsStore()

  return (
    <header className="bg-white border-b border-gray-200 shadow-sm">
      <div className="container mx-auto px-6 py-4 flex items-center justify-between">
        <div className="flex items-center gap-4">
          <button
            onClick={toggleSidebar}
            className="p-2 hover:bg-gray-100 rounded-lg transition-colors"
            title="Toggle sidebar"
          >
            <svg className="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4 6h16M4 12h16M4 18h16" />
            </svg>
          </button>
          <h1 className="text-2xl font-bold text-gray-900">VRSky</h1>
        </div>

        <div className="flex items-center gap-6">
          <div className="text-sm text-gray-600">
            <span className="font-medium">{connections.length}</span> connections
          </div>
          <div className="flex items-center gap-2 px-3 py-1 bg-green-50 border border-green-200 rounded-full">
            <div className="w-2 h-2 bg-green-500 rounded-full"></div>
            <span className="text-xs font-medium text-green-700">Healthy</span>
          </div>
        </div>
      </div>
    </header>
  )
}
